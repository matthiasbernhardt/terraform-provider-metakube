package metakube

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/customdiff"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"

	"github.com/syseleven/go-metakube/client/datacenter"
	"github.com/syseleven/go-metakube/client/project"
	"github.com/syseleven/go-metakube/models"
)

func metakubeResourceCluster() *schema.Resource {
	return &schema.Resource{
		CreateContext: metakubeResourceClusterCreate,
		ReadContext:   metakubeResourceClusterRead,
		UpdateContext: metakubeResourceClusterUpdate,
		DeleteContext: metakubeResourceClusterDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"project_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Reference project identifier",
			},
			"dc_name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Data center name",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Cluster name",
			},
			"labels": {
				Type:        schema.TypeMap,
				Optional:    true,
				Description: "Labels added to cluster",
			},
			"sshkeys": {
				Type:        schema.TypeSet,
				Optional:    true,
				Description: "SSH keys attached to nodes",
				Elem: &schema.Schema{
					Type:         schema.TypeString,
					ValidateFunc: validation.NoZeroValues,
				},
			},
			"spec": {
				Type:        schema.TypeList,
				Required:    true,
				MaxItems:    1,
				Description: "Cluster specification",
				Elem: &schema.Resource{
					Schema: metakubeResourceClusterSpecFields(),
				},
			},
			"creation_timestamp": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Creation timestamp",
			},
			"deletion_timestamp": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Deletion timestamp",
			},
			"kube_config": {
				Type:     schema.TypeString,
				Computed: true,
			},
		},
		CustomizeDiff: customdiff.All(customdiff.ForceNewIfChange(
			"spec.0.version",
			metakubeResourceClusterIsVersionDowngraded)),
	}
}

func metakubeResourceClusterIsVersionDowngraded(_ context.Context, old, new, meta interface{}) bool {
	// "version" can only be upgraded to newer versions, so we must create a new resource
	// if it is decreased.
	newVer, err := version.NewVersion(new.(string))
	if err != nil {
		return false
	}

	oldVer, err := version.NewVersion(old.(string))
	if err != nil {
		return false
	}

	return newVer.LessThan(oldVer)
}

func metakubeResourceClusterCreate(ctx context.Context, d *schema.ResourceData, m interface{}) (diagnostics diag.Diagnostics) {
	meta := m.(*metakubeProviderMeta)
	retDiags := metakubeResourceClusterValidateClusterFields(ctx, d, meta)
	spec := d.Get("spec").([]interface{})
	dcname := d.Get("dc_name").(string)
	clusterSpec := metakubeResourceClusterExpandSpec(spec, dcname)
	createClusterSpec := &models.CreateClusterSpec{
		Cluster: &models.Cluster{
			Name:   d.Get("name").(string),
			Spec:   clusterSpec,
			Type:   "kubernetes",
			Labels: metakubeResourceClusterLabels(d),
		},
	}
	if n := clusterSpec.ClusterNetwork; n != nil {
		if n.DNSDomain != "" {
			createClusterSpec.DNSDomain = n.DNSDomain
		}
		if v := clusterSpec.ClusterNetwork.Pods; v != nil {
			if len(v.CIDRBlocks) == 1 {
				createClusterSpec.PodsCIDR = v.CIDRBlocks[0]
			}
			if len(v.CIDRBlocks) > 1 {
				retDiags = append(retDiags, diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  "API returned multiple pods CIDRs",
				})
			}
		}
		if v := clusterSpec.ClusterNetwork.Services; v != nil {
			if len(v.CIDRBlocks) == 1 {
				createClusterSpec.ServicesCIDR = v.CIDRBlocks[0]
			}
			if len(v.CIDRBlocks) > 1 {
				retDiags = append(retDiags, diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  "API returned multiple services CIDRs",
				})
			}
		}
	}

	sshkeys := metakubeResourceClusterSSHKeys(d)
	if len(sshkeys) > 0 && !d.Get("spec.0.enable_ssh_agent").(bool) {
		return append(retDiags, diag.Diagnostic{
			Severity:      diag.Error,
			AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("enable_ssh_agent"),
			Summary:       "SSH Agent must be enabled in order to automatically manage ssh keys",
		})
	}

	projectID := d.Get("project_id").(string)
	p := project.NewCreateClusterV2Params().WithProjectID(projectID).WithBody(createClusterSpec)
	r, err := meta.client.Project.CreateClusterV2(p, meta.auth)
	if err != nil {
		return diag.Errorf("unable to create cluster for project '%s': %s", projectID, stringifyResponseError(err))
	}
	d.SetId(r.Payload.ID)

	if diags := assignSSHKeysToCluster(projectID, r.Payload.ID, sshkeys, meta); diags != nil {
		return diags
	}

	if err := metakubeResourceClusterWaitForReady(ctx, meta, d, projectID, d.Id()); err != nil {
		return diag.Errorf("cluster '%s' is not ready: %v", r.Payload.ID, err)
	}

	return metakubeResourceClusterRead(ctx, d, m)
}

func metakubeResourceClusterLabels(d *schema.ResourceData) map[string]string {
	var labels map[string]string
	if v := d.Get("labels"); v != nil {
		labels = make(map[string]string)
		m := d.Get("labels").(map[string]interface{})
		for k, v := range m {
			labels[k] = v.(string)
		}
	}
	return labels
}

func metakubeResourceClusterSSHKeys(d *schema.ResourceData) []string {
	var ret []string
	for _, v := range d.Get("sshkeys").(*schema.Set).List() {
		ret = append(ret, v.(string))
	}
	return ret
}

func metakubeResourceClusterFindDatacenterByName(k *metakubeProviderMeta, d *schema.ResourceData) (*models.Datacenter, diag.Diagnostics) {
	name := d.Get("dc_name").(string)
	p := datacenter.NewListDatacentersParams()
	r, err := k.client.Datacenter.ListDatacenters(p, k.auth)
	if err != nil {
		return nil, diag.Errorf("Can't list datacenters: %s", stringifyResponseError(err))
	}

	available := make([]string, 0)
	openstackCluster := metakubeResourceClusterIsOpenstack(d)
	awsCluster := metakubeResourceClusterIsAWS(d)
	azureCluster := metakubeResourceClusterIsAzure(d)
	for _, dc := range r.Payload {
		openstackDatacenter := dc.Spec.Openstack != nil
		awsDatacenter := dc.Spec.Aws != nil
		azureDatacenter := dc.Spec.Azure != nil
		if (openstackCluster && openstackDatacenter) ||
			(awsCluster && awsDatacenter) ||
			(azureCluster && azureDatacenter) {
			available = append(available, dc.Metadata.Name)
		}
		if dc.Spec.Seed != "" && dc.Metadata.Name == name {
			return dc, nil
		}
	}

	return nil, diag.Diagnostics{{
		Severity:      diag.Error,
		Summary:       "Unknown datacenter",
		AttributePath: cty.Path{cty.GetAttrStep{Name: "dc_name"}},
		Detail:        fmt.Sprintf("Please select one of available datacenters for the provider - %v", available),
	}}
}

func metakubeResourceClusterIsOpenstack(d *schema.ResourceData) bool {
	return d.Get("spec.0.cloud.0.openstack.#").(int) == 1
}

func metakubeResourceClusterIsAzure(d *schema.ResourceData) bool {
	return d.Get("spec.0.cloud.0.azure.#").(int) == 1
}

func metakubeResourceClusterIsAWS(d *schema.ResourceData) bool {
	return d.Get("spec.0.cloud.0.aws.#").(int) == 1
}

func metakubeResourceClusterRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	k := m.(*metakubeProviderMeta)
	projectID := d.Get("project_id").(string)
	if projectID == "" {
		var err error
		projectID, err = metakubeResourceClusterFindProjectID(ctx, d.Id(), k)
		if err != nil {
			return diag.FromErr(err)
		}
		if projectID == "" {
			d.SetId("")
			k.log.Info("owner project not found")
			return nil
		}
		k.log.Debugf("found cluster in project '%s'", projectID)
	}
	p := project.NewGetClusterV2Params().WithContext(ctx).WithProjectID(projectID).WithClusterID(d.Id())
	r, err := k.client.Project.GetClusterV2(p, k.auth)
	if metakubeResourceClusterResponseNotFound(err) {
		k.log.Infof("removing cluster '%s', could not find the resource", d.Id())
		d.SetId("")
		return nil
	}
	if err != nil {
		// TODO: check the cluster API code
		// when cluster does not exist but it is in terraform state file
		// the GET request returns 500 http code instead of 404, probably it's a bug
		// because of that manual action to clean terraform state file is required

		k.log.Debugf("get cluster: %v", err)
		return diag.Errorf("unable to get cluster '%s/%s': %s", projectID, d.Id(), stringifyResponseError(err))
	}

	_ = d.Set("project_id", projectID)
	_ = d.Set("dc_name", r.Payload.Spec.Cloud.DatacenterName)

	labels, diagnostics := metakubeResourceClusterExcludeSystemLabels(k, projectID, r.Payload.Labels)
	if diagnostics != nil {
		return diagnostics
	}
	if err := d.Set("labels", labels); err != nil {
		return diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       "Invalid value",
			AttributePath: cty.Path{cty.GetAttrStep{Name: "labels"}},
		}}
	}

	_ = d.Set("name", r.Payload.Name)

	values := readClusterPreserveValues(d)
	specFlattened := metakubeResourceClusterFlattenSpec(values, r.Payload.Spec)
	if err = d.Set("spec", specFlattened); err != nil {
		return diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       "Invalid value",
			AttributePath: cty.Path{cty.GetAttrStep{Name: "spec"}},
		}}
	}

	_ = d.Set("creation_timestamp", r.Payload.CreationTimestamp.String())

	_ = d.Set("deletion_timestamp", r.Payload.DeletionTimestamp.String())

	keys, diagnostics := metakubeClusterGetAssignedSSHKeys(ctx, d, k)
	if diagnostics != nil {
		return diagnostics
	}
	if err := d.Set("sshkeys", keys); err != nil {
		return diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       "Invalid value",
			AttributePath: cty.GetAttrPath("sshkeys"),
		}}
	}

	kubeConfigParams := project.NewGetClusterKubeconfigV2Params()
	kubeConfigParams.SetContext(ctx)
	kubeConfigParams.SetProjectID(projectID)
	kubeConfigParams.SetClusterID(d.Id())
	ret, err := k.client.Project.GetClusterKubeconfigV2(kubeConfigParams, k.auth)
	if err != nil {
		return diag.Diagnostics{{
			Severity:      diag.Warning,
			Summary:       fmt.Sprintf("Failed to get kube_config: %v", stringifyResponseError(err)),
			AttributePath: cty.GetAttrPath("kube_config"),
		}}
	}
	err = d.Set("kube_config", string(ret.Payload))
	if err != nil {
		k.log.Error(err)
	}

	return nil
}

func metakubeResourceClusterFindProjectID(ctx context.Context, id string, meta *metakubeProviderMeta) (string, error) {
	res, err := meta.client.Project.ListProjects(project.NewListProjectsParams(), meta.auth)
	if err != nil {
		return "", fmt.Errorf("list projects: %v", err)
	}

	for _, project := range res.Payload {
		ok, err := metakubeResourceClusterBelongsToProject(ctx, project.ID, id, meta)
		if ok {
			return project.ID, nil
		}
		if err != nil {
			return "", err
		}
	}

	meta.log.Info("owner project for cluster with id(%s) not found", id)
	return "", nil
}

func metakubeResourceClusterBelongsToProject(ctx context.Context, prj, id string, meta *metakubeProviderMeta) (bool, error) {
	prms := project.NewListClustersV2Params().WithContext(ctx).WithProjectID(prj)
	res, err := meta.client.Project.ListClustersV2(prms, meta.auth)
	if err != nil {
		meta.log.Debugf("lookup owner project: list clusters: %v", err)
		return false, fmt.Errorf("list clusters: %v", err)
	}
	for _, item := range res.Payload {
		if item.ID == id {
			return true, nil
		}
	}
	return false, nil
}

func metakubeResourceClusterResponseNotFound(err error) bool {
	if err == nil {
		return false
	}

	e, ok := err.(*project.GetClusterV2Default)
	if !ok {
		return false
	}

	// All api replies and errors, that nevertheless indicate cluster was deleted.
	return e.Code() == http.StatusNotFound
}

// metakubeResourceClusterExcludeSystemLabels excludes labels defined in project.
// Project labels propagated to clusters. For better predictability of
// cluster's labels changes, project's labels are excluded from cluster state.
func metakubeResourceClusterExcludeSystemLabels(k *metakubeProviderMeta, projectID string, labels map[string]string) (map[string]string, diag.Diagnostics) {
	p := project.NewGetProjectParams().WithProjectID(projectID)
	r, err := k.client.Project.GetProject(p, k.auth)
	if err != nil {
		return nil, diag.Errorf("get project details %v", stringifyResponseError(err))
	}
	for key := range r.Payload.Labels {
		delete(labels, key)
	}

	return labels, nil
}

func metakubeClusterGetAssignedSSHKeys(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) ([]string, diag.Diagnostics) {
	projectID := d.Get("project_id").(string)
	p := project.NewListSSHKeysAssignedToClusterV2Params().WithProjectID(projectID).WithClusterID(d.Id())
	ret, err := k.client.Project.ListSSHKeysAssignedToClusterV2(p, k.auth)
	if err != nil {
		return nil, diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       fmt.Sprintf("List project keys error %v", stringifyResponseError(err)),
			AttributePath: cty.Path{cty.GetAttrStep{Name: "sshkeys"}},
		}}
	}

	var ids []string
	for _, v := range ret.Payload {
		ids = append(ids, v.ID)
	}
	return ids, nil
}

// clusterPreserveValues helps avoid misleading diffs during read phase.
// API does not return some important fields, like access key or password.
// To avoid diffs because of missing field when API reply is flattened we manually set
// values for fields to preserve in flattened object before committing it to state.
type clusterPreserveValues struct {
	openstack *clusterOpenstackPreservedValues
	// API returns empty spec for Azure and AWS clusters, so we just preserve values used for creation
	azure *models.AzureCloudSpec
	aws   *models.AWSCloudSpec
}

type clusterOpenstackPreservedValues struct {
	openstackUsername                     interface{}
	openstackPassword                     interface{}
	openstackTenant                       interface{}
	openstackApplicationCredentialsID     interface{}
	openstackApplicationCredentialsSecret interface{}
}

func readClusterPreserveValues(d *schema.ResourceData) clusterPreserveValues {
	key := func(s string) string {
		return fmt.Sprint("spec.0.cloud.0.", s)
	}
	var openstack *clusterOpenstackPreservedValues
	if _, ok := d.GetOk(key("openstack.0")); ok {
		openstack = &clusterOpenstackPreservedValues{
			openstackUsername:                     d.Get(key("openstack.0.username")),
			openstackPassword:                     d.Get(key("openstack.0.password")),
			openstackTenant:                       d.Get(key("openstack.0.tenant")),
			openstackApplicationCredentialsID:     d.Get(key("openstack.0.application_credentials_id")),
			openstackApplicationCredentialsSecret: d.Get(key("openstack.0.application_credentials_secret")),
		}
	}

	var azure *models.AzureCloudSpec
	if _, ok := d.GetOk(key("azure.0")); ok {
		azure = &models.AzureCloudSpec{
			AvailabilitySet:        d.Get(key("azure.0.availability_set")).(string),
			ClientID:               d.Get(key("azure.0.client_id")).(string),
			ClientSecret:           d.Get(key("azure.0.client_secret")).(string),
			SubscriptionID:         d.Get(key("azure.0.subscription_id")).(string),
			TenantID:               d.Get(key("azure.0.tenant_id")).(string),
			ResourceGroup:          d.Get(key("azure.0.resource_group")).(string),
			RouteTableName:         d.Get(key("azure.0.route_table")).(string),
			SecurityGroup:          d.Get(key("azure.0.security_group")).(string),
			SubnetName:             d.Get(key("azure.0.subnet")).(string),
			VNetName:               d.Get(key("azure.0.vnet")).(string),
			OpenstackBillingTenant: d.Get(key("azure.0.openstack_billing_tenant")).(string),
		}
	}

	var aws *models.AWSCloudSpec
	if _, ok := d.GetOk(key("aws.0")); ok {
		aws = &models.AWSCloudSpec{
			AccessKeyID:            d.Get(key("aws.0.access_key_id")).(string),
			SecretAccessKey:        d.Get(key("aws.0.secret_access_key")).(string),
			VPCID:                  d.Get(key("aws.0.vpc_id")).(string),
			SecurityGroupID:        d.Get(key("aws.0.security_group_id")).(string),
			RouteTableID:           d.Get(key("aws.0.route_table_id")).(string),
			InstanceProfileName:    d.Get(key("aws.0.instance_profile_name")).(string),
			ControlPlaneRoleARN:    d.Get(key("aws.0.role_arn")).(string),
			OpenstackBillingTenant: d.Get(key("aws.0.openstack_billing_tenant")).(string),
		}
	}

	return clusterPreserveValues{
		openstack,
		azure,
		aws,
	}
}

func metakubeResourceClusterUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	k := m.(*metakubeProviderMeta)
	projectID := d.Get("project_id").(string)

	retDiags := metakubeResourceClusterValidateClusterFields(ctx, d, k)

	_, diagnostics := metakubeResourceClusterFindDatacenterByName(k, d)
	// TODO: delete composed diagnostics, seems to be useless at the moment.
	retDiags = append(retDiags, diagnostics...)
	if len(retDiags) != 0 {
		return retDiags
	}

	if d.HasChanges("name", "labels", "spec") {
		if err := metakubeResourceClusterSendPatchReq(ctx, d, k); err != nil {
			return diag.FromErr(err)
		}
	}
	if d.HasChange("sshkeys") {
		if err := updateClusterSSHKeys(ctx, d, k); err != nil {
			return err
		}
	}

	if err := metakubeResourceClusterWaitForReady(ctx, k, d, projectID, d.Id()); err != nil {
		return diag.Errorf("cluster '%s' is not ready: %v", d.Id(), err)
	}

	return metakubeResourceClusterRead(ctx, d, m)
}

func metakubeResourceClusterSendPatchReq(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) error {
	projectID := d.Get("project_id").(string)
	p := project.NewPatchClusterV2Params()
	p.SetContext(ctx)
	p.SetProjectID(projectID)
	p.SetClusterID(d.Id())
	name := d.Get("name").(string)
	labels := metakubeResourceClusterGetLabelsChange(d)
	clusterSpec := metakubeResourceClusterExpandSpec(d.Get("spec").([]interface{}), d.Get("dc_name").(string))
	p.SetPatch(map[string]interface{}{
		"name":   name,
		"labels": labels,
		"spec":   clusterSpec,
	})

	err := resource.RetryContext(ctx, d.Timeout(schema.TimeoutUpdate), func() *resource.RetryError {
		_, err := k.client.Project.PatchClusterV2(p, k.auth)
		if err != nil {
			if e, ok := err.(*project.PatchClusterV2Default); ok && e.Code() == http.StatusConflict {
				return resource.RetryableError(fmt.Errorf("cluster patch conflict: %v", err))
			}
			return resource.NonRetryableError(fmt.Errorf("patch cluster '%s': %v", d.Id(), err))
		}
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

func metakubeResourceClusterGetLabelsChange(d *schema.ResourceData) map[string]interface{} {
	oldLabels, newLabels := d.GetChange("labels")
	var oldLabelsMap, newLabelsMap map[string]interface{}
	if oldLabels != nil {
		oldLabelsMap = oldLabels.(map[string]interface{})
	}
	if newLabels != nil {
		newLabelsMap = newLabels.(map[string]interface{})
	} else {
		newLabelsMap = make(map[string]interface{})
	}

	for k := range oldLabelsMap {
		if _, ok := newLabelsMap[k]; !ok {
			newLabelsMap[k] = nil
		}
	}

	return newLabelsMap
}

func updateClusterSSHKeys(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
	projectID := d.Get("project_id").(string)
	var unassigned, assign []string
	prev, cur := d.GetChange("sshkeys")

	for _, id := range prev.(*schema.Set).List() {
		if !cur.(*schema.Set).Contains(id) {
			unassigned = append(unassigned, id.(string))
		}
	}

	for _, id := range cur.(*schema.Set).List() {
		if !prev.(*schema.Set).Contains(id) {
			assign = append(assign, id.(string))
		}
	}

	for _, id := range unassigned {
		p := project.NewDetachSSHKeyFromClusterV2Params()
		p.SetProjectID(projectID)
		p.SetClusterID(d.Id())
		p.SetKeyID(id)
		_, err := k.client.Project.DetachSSHKeyFromClusterV2(p, k.auth)
		if err != nil {
			if e, ok := err.(*project.DetachSSHKeyFromClusterV2Default); ok && e.Code() == http.StatusNotFound {
				continue
			}
			return diag.FromErr(err)
		}
	}

	if err := assignSSHKeysToCluster(projectID, d.Id(), assign, k); err != nil {
		return err
	}

	return nil
}

func assignSSHKeysToCluster(projectID, clusterID string, sshkeyIDs []string, k *metakubeProviderMeta) diag.Diagnostics {
	for _, id := range sshkeyIDs {
		p := project.NewAssignSSHKeyToClusterV2Params().WithProjectID(projectID).WithClusterID(clusterID).WithKeyID(id)
		_, err := k.client.Project.AssignSSHKeyToClusterV2(p, k.auth)
		if err != nil {
			return diag.Diagnostics{{
				Severity:      diag.Error,
				Summary:       fmt.Sprintf("Can't assign sshkeys to cluster '%s': %v", clusterID, err),
				AttributePath: cty.Path{cty.GetAttrStep{Name: "sshkeys"}},
			}}
		}
	}

	return nil
}

func metakubeResourceClusterWaitForReady(ctx context.Context, k *metakubeProviderMeta, d *schema.ResourceData, projectID, clusterID string) error {
	return resource.RetryContext(ctx, d.Timeout(schema.TimeoutCreate), func() *resource.RetryError {

		p := project.NewGetClusterHealthV2Params()
		p.SetContext(ctx)
		p.SetProjectID(projectID)
		p.SetClusterID(clusterID)

		r, err := k.client.Project.GetClusterHealthV2(p, k.auth)
		if err != nil {
			return resource.RetryableError(fmt.Errorf("unable to get cluster '%s' health: %s", d.Id(), stringifyResponseError(err)))
		}

		const up models.HealthStatus = 1

		if r.Payload.Apiserver == up &&
			r.Payload.CloudProviderInfrastructure == up &&
			r.Payload.Controller == up &&
			r.Payload.Etcd == up &&
			r.Payload.MachineController == up &&
			r.Payload.Scheduler == up &&
			r.Payload.UserClusterControllerManager == up {
			return nil
		}

		k.log.Debugf("waiting for cluster '%s' to be ready, %+v", d.Id(), r.Payload)
		return resource.RetryableError(fmt.Errorf("waiting for cluster '%s' to be ready", d.Id()))
	})
}

func metakubeResourceClusterDelete(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	k := m.(*metakubeProviderMeta)
	projectID := d.Get("project_id").(string)
	p := project.NewDeleteClusterV2Params()

	p.SetProjectID(projectID)
	p.SetClusterID(d.Id())

	deleteSent := false
	err := resource.RetryContext(ctx, d.Timeout(schema.TimeoutDelete), func() *resource.RetryError {
		if !deleteSent {
			_, err := k.client.Project.DeleteClusterV2(p, k.auth)
			if err != nil {
				if e, ok := err.(*project.DeleteClusterV2Default); ok {
					if e.Code() == http.StatusConflict {
						return resource.RetryableError(err)
					}
					if e.Code() == http.StatusNotFound {
						return nil
					}
				}
				if _, ok := err.(*project.DeleteClusterV2Forbidden); ok {
					return nil
				}
				return resource.NonRetryableError(fmt.Errorf("unable to delete cluster '%s': %s", d.Id(), stringifyResponseError(err)))
			}
			deleteSent = true
		}
		p := project.NewGetClusterV2Params()

		p.SetProjectID(projectID)
		p.SetClusterID(d.Id())

		r, err := k.client.Project.GetClusterV2(p, k.auth)
		if err != nil {
			if e, ok := err.(*project.GetClusterV2Default); ok && e.Code() == http.StatusNotFound {
				k.log.Debugf("cluster '%s' has been destroyed, returned http code: %d", d.Id(), e.Code())
				return nil
			}
			if _, ok := err.(*project.GetClusterV2Forbidden); ok {
				return nil
			}
			return resource.NonRetryableError(fmt.Errorf("unable to get cluster '%s': %s", d.Id(), stringifyResponseError(err)))
		}

		k.log.Debugf("cluster '%s' deletion in progress, deletionTimestamp: %s",
			d.Id(), r.Payload.DeletionTimestamp.String())
		return resource.RetryableError(fmt.Errorf("cluster '%s' deletion in progress", d.Id()))
	})
	if err != nil {
		return diag.FromErr(err)
	}
	return nil
}
