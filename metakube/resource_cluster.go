package metakube

import (
	"context"
	"fmt"
	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"net/http"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/customdiff"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/syseleven/terraform-provider-metakube/go-metakube/client/datacenter"
	"github.com/syseleven/terraform-provider-metakube/go-metakube/client/project"
	"github.com/syseleven/terraform-provider-metakube/go-metakube/models"
)

const (
	healthStatusUp models.HealthStatus = 1
)

var supportedProviders = []string{"aws", "openstack", "azure"}

func resourceCluster() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceClusterCreate,
		ReadContext:   resourceClusterRead,
		UpdateContext: resourceClusterUpdate,
		DeleteContext: resourceClusterDelete,
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
					Schema: clusterSpecFields(),
				},
			},

			"credential": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Cluster access credential",
			},

			"type": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Default:     "kubernetes",
				Description: "Cloud orchestrator, either Kubernetes or OpenShift",
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
			// TODO: uncomment once `no consumer: "application/yaml"` error in metakube client is fixed.
			// "kube_config": kubernetesConfigSchema(),
		},

		CustomizeDiff: customdiff.All(
			customdiff.ForceNewIfChange("spec.0.version", func(_ context.Context, old, new, meta interface{}) bool {
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

				if newVer.LessThan(oldVer) {
					return true
				}
				return false
			}),
		),
	}
}

func resourceClusterCreate(ctx context.Context, d *schema.ResourceData, m interface{}) (diagnostics diag.Diagnostics) {
	k := m.(*metakubeProviderMeta)

	allDiagnostics := validateClusterFields(ctx, d, k)

	dc, diagnostics := getDatacenterByName(k, d)
	allDiagnostics = append(allDiagnostics, diagnostics...)
	if len(allDiagnostics) != 0 {
		return allDiagnostics
	}

	pID := d.Get("project_id").(string)
	p := project.NewCreateClusterParams()
	clusterSpec := expandClusterSpec(d.Get("spec").([]interface{}), d.Get("dc_name").(string))
	createClusterSpec := &models.CreateClusterSpec{
		Cluster: &models.Cluster{
			Name:       d.Get("name").(string),
			Spec:       clusterSpec,
			Type:       d.Get("type").(string),
			Labels:     getLabels(d),
			Credential: d.Get("credential").(string),
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
				allDiagnostics = append(allDiagnostics, diag.Diagnostic{
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
				allDiagnostics = append(allDiagnostics, diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  "API returned multiple services CIDRs",
				})
			}
		}
	}

	p.SetProjectID(pID)
	p.SetDC(dc.Spec.Seed)
	p.SetBody(createClusterSpec)

	r, err := k.client.Project.CreateCluster(p, k.auth)
	if err != nil {
		return diag.Errorf("unable to create cluster for project '%s': %s", pID, getErrorResponse(err))
	}
	d.SetId(metakubeClusterMakeID(d.Get("project_id").(string), dc.Spec.Seed, r.Payload.ID))

	raw := d.Get("sshkeys").(*schema.Set).List()
	var sshkeys []string
	for _, v := range raw {
		sshkeys = append(sshkeys, v.(string))
	}
	if diags := assignSSHKeysToCluster(pID, dc.Spec.Seed, r.Payload.ID, sshkeys, k); diags != nil {
		return diags
	}

	projectID, seedDC, clusterID, err := metakubeClusterParseID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}
	if err := waitClusterReady(ctx, k, d, projectID, seedDC, clusterID); err != nil {
		return diag.Errorf("cluster '%s' is not ready: %v", r.Payload.ID, err)
	}

	return resourceClusterRead(ctx, d, m)
}

func metakubeClusterMakeID(project, seedDC, id string) string {
	return fmt.Sprintf("%s:%s:%s", project, seedDC, id)
}

func metakubeClusterParseID(id string) (string, string, string, error) {
	parts := strings.SplitN(id, ":", 3)

	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("unexpected format of ID (%s), expected project_id:seed_dc:id", id)
	}

	return parts[0], parts[1], parts[2], nil
}

func getLabels(d *schema.ResourceData) map[string]string {
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

func getDatacenterByName(k *metakubeProviderMeta, d *schema.ResourceData) (*models.Datacenter, diag.Diagnostics) {
	name := d.Get("dc_name").(string)
	p := datacenter.NewListDatacentersParams()
	r, err := k.client.Datacenter.ListDatacenters(p, k.auth)
	if err != nil {
		if e, ok := err.(*datacenter.ListDatacentersDefault); ok && errorMessage(e.Payload) != "" {
			return nil, diag.Diagnostics{{
				Severity:      diag.Error,
				Summary:       fmt.Sprintf("Can't list datacenters: %s", errorMessage(e.Payload)),
				AttributePath: cty.Path{cty.GetAttrStep{Name: "dc_name"}},
			}}
		}
		return nil, diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       fmt.Sprintf("Can't list datacenters: %s", err),
			AttributePath: cty.Path{cty.GetAttrStep{Name: "dc_name"}},
		}}
	}

	available := make([]string, 0)
	for _, v := range r.Payload {
		if (isOpenstack(d) && v.Spec.Openstack != nil) || (isAWS(d) && v.Spec.Aws != nil) || (isAzure(d) && v.Spec.Azure != nil) {
			available = append(available, v.Metadata.Name)
		}
		if v.Spec.Seed != "" && v.Metadata.Name == name {
			return v, nil
		}
	}

	return nil, diag.Diagnostics{{
		Severity:      diag.Error,
		Summary:       "Unknown datacenter",
		AttributePath: cty.Path{cty.GetAttrStep{Name: "dc_name"}},
		Detail:        fmt.Sprintf("Please select one of available datacenters for the provider - %v", available),
	}}
}

func isOpenstack(d *schema.ResourceData) bool {
	return d.Get("spec.0.cloud.0.openstack.#").(int) == 1
}

func isAzure(d *schema.ResourceData) bool {
	return d.Get("spec.0.cloud.0.azure.#").(int) == 1
}

func isAWS(d *schema.ResourceData) bool {
	return d.Get("spec.0.cloud.0.aws.#").(int) == 1
}

func resourceClusterRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	k := m.(*metakubeProviderMeta)
	p := project.NewGetClusterParams()
	projectID, seedDC, clusterID, err := metakubeClusterParseID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}
	p.SetProjectID(projectID)
	p.SetDC(seedDC)
	p.SetClusterID(clusterID)

	r, err := k.client.Project.GetCluster(p, k.auth)
	if getClusterErrResourceIsDeleted(err) {
		k.log.Infof("removing cluster '%s' from terraform state file, could not find the resource", d.Id())
		d.SetId("")
		return nil
	}
	if err != nil {
		if e, ok := err.(*project.GetClusterDefault); ok && e.Code() == http.StatusNotFound {
			k.log.Infof("removing cluster '%s' from terraform state file, could not find the resource", d.Id())
			d.SetId("")
			return nil
		}

		// TODO: check the cluster API code
		// when cluster does not exist but it is in terraform state file
		// the GET request returns 500 http code instead of 404, probably it's a bug
		// because of that manual action to clean terraform state file is required

		return diag.Errorf("unable to get cluster '%s': %s", d.Id(), getErrorResponse(err))
	}

	_ = d.Set("project_id", projectID)
	_ = d.Set("dc_name", r.Payload.Spec.Cloud.DatacenterName)

	labels, diagnostics := excludeProjectLabels(k, projectID, r.Payload.Labels)
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

	// TODO: check why API returns an empty credential field even if it is set
	//err = d.Set("credential", r.Payload.Credential)
	//if err != nil {
	//	return err
	//}

	_ = d.Set("type", r.Payload.Type)

	values := readClusterPreserveValues(d)
	specFlattened := flattenClusterSpec(values, r.Payload.Spec)
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
			AttributePath: cty.Path{cty.GetAttrStep{Name: "sshkeys"}},
		}}
	}

	return nil
}

func getClusterErrResourceIsDeleted(err error) bool {
	if err == nil {
		return false
	}

	e, ok := err.(*project.GetClusterDefault)
	if !ok {
		return false
	}

	// All api replies and errors, that nevertheless indicate cluster was deleted.
	return e.Code() == http.StatusNotFound
}

// excludeProjectLabels excludes labels defined in project.
// Project labels propagated to clusters. For better predictability of
// cluster's labels changes, project's labels are excluded from cluster state.
func excludeProjectLabels(k *metakubeProviderMeta, projectID string, allLabels map[string]string) (map[string]string, diag.Diagnostics) {
	p := project.NewGetProjectParams()
	p.SetProjectID(projectID)

	r, err := k.client.Project.GetProject(p, k.auth)
	if err != nil {
		return nil, diag.Errorf("get project details %v", getErrorResponse(err))
	}

	for k := range r.Payload.Labels {
		delete(allLabels, k)
	}

	return allLabels, nil
}

func metakubeClusterGetAssignedSSHKeys(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) ([]string, diag.Diagnostics) {
	p := project.NewListSSHKeysAssignedToClusterParams()
	projectID, seedDC, clusterID, err := metakubeClusterParseID(d.Id())
	if err != nil {
		return nil, diag.FromErr(err)
	}
	p.SetProjectID(projectID)
	p.SetDC(seedDC)
	p.SetClusterID(clusterID)
	ret, err := k.client.Project.ListSSHKeysAssignedToCluster(p, k.auth)
	if err != nil {
		return nil, diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       fmt.Sprintf("List project keys error %v", getErrorResponse(err)),
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
	openstackUsername interface{}
	openstackPassword interface{}
	openstackTenant   interface{}
}

func readClusterPreserveValues(d *schema.ResourceData) clusterPreserveValues {
	key := func(s string) string {
		return fmt.Sprint("spec.0.cloud.0.", s)
	}
	var openstack *clusterOpenstackPreservedValues
	if _, ok := d.GetOk(key("openstack.0")); ok {
		openstack = &clusterOpenstackPreservedValues{
			openstackUsername: d.Get(key("openstack.0.username")),
			openstackPassword: d.Get(key("openstack.0.password")),
			openstackTenant:   d.Get(key("openstack.0.tenant")),
		}
	}

	var azure *models.AzureCloudSpec
	if _, ok := d.GetOk(key("azure.0")); ok {
		azure = &models.AzureCloudSpec{
			AvailabilitySet: d.Get(key("azure.0.availability_set")).(string),
			ClientID:        d.Get(key("azure.0.client_id")).(string),
			ClientSecret:    d.Get(key("azure.0.client_secret")).(string),
			SubscriptionID:  d.Get(key("azure.0.subscription_id")).(string),
			TenantID:        d.Get(key("azure.0.tenant_id")).(string),
			ResourceGroup:   d.Get(key("azure.0.resource_group")).(string),
			RouteTableName:  d.Get(key("azure.0.route_table")).(string),
			SecurityGroup:   d.Get(key("azure.0.security_group")).(string),
			SubnetName:      d.Get(key("azure.0.subnet")).(string),
			VNetName:        d.Get(key("azure.0.vnet")).(string),
		}
	}

	var aws *models.AWSCloudSpec
	if _, ok := d.GetOk(key("aws.0")); ok {
		aws = &models.AWSCloudSpec{
			AccessKeyID:         d.Get(key("aws.0.access_key_id")).(string),
			SecretAccessKey:     d.Get(key("aws.0.secret_access_key")).(string),
			VPCID:               d.Get(key("aws.0.vpc_id")).(string),
			SecurityGroupID:     d.Get(key("aws.0.security_group_id")).(string),
			RouteTableID:        d.Get(key("aws.0.route_table_id")).(string),
			InstanceProfileName: d.Get(key("aws.0.instance_profile_name")).(string),
			ControlPlaneRoleARN: d.Get(key("aws.0.role_arn")).(string),
		}
	}

	return clusterPreserveValues{
		openstack,
		azure,
		aws,
	}
}

func resourceClusterUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	k := m.(*metakubeProviderMeta)

	allDiagnostics := validateClusterFields(ctx, d, k)

	_, diagnostics := getDatacenterByName(k, d)
	allDiagnostics = append(allDiagnostics, diagnostics...)
	if len(allDiagnostics) != 0 {
		return allDiagnostics
	}

	if d.HasChanges("name", "labels", "spec") {
		if err := patchClusterFields(ctx, d, k); err != nil {
			return diag.FromErr(err)
		}
	}
	if d.HasChange("sshkeys") {
		if dErr := updateClusterSSHKeys(ctx, d, k); dErr != nil {
			return dErr
		}
	}

	projectID, seedDC, clusterID, err := metakubeClusterParseID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}
	if err := waitClusterReady(ctx, k, d, projectID, seedDC, clusterID); err != nil {
		return diag.Errorf("cluster '%s' is not ready: %v", d.Id(), err)
	}

	return resourceClusterRead(ctx, d, m)
}

func patchClusterFields(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) error {
	p := project.NewPatchClusterParams()
	projectID, seedDC, clusterID, err := metakubeClusterParseID(d.Id())
	if err != nil {
		return err
	}
	p.SetContext(ctx)
	p.SetProjectID(projectID)
	p.SetDC(seedDC)
	p.SetClusterID(clusterID)
	name := d.Get("name").(string)
	labels := d.Get("labels")
	clusterSpec := expandClusterSpec(d.Get("spec").([]interface{}), d.Get("dc_name").(string))
	// p.SetPatch(newClusterPatch(name, version, auditLogging, labels))
	p.SetPatch(map[string]interface{}{
		"name":   name,
		"labels": labels,
		"spec":   clusterSpec,
	})

	err = resource.RetryContext(ctx, d.Timeout(schema.TimeoutUpdate), func() *resource.RetryError {
		_, err := k.client.Project.PatchCluster(p, k.auth)
		if err != nil {
			if e, ok := err.(*project.PatchClusterDefault); ok && e.Code() == http.StatusConflict {
				return resource.RetryableError(fmt.Errorf("cluster patch conflict: %v", err))
			} else if ok && errorMessage(e.Payload) != "" {
				return resource.NonRetryableError(fmt.Errorf("patch cluster '%s': %s", d.Id(), errorMessage(e.Payload)))
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

func updateClusterSSHKeys(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
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

	projectID, seedDC, clusterID, err := metakubeClusterParseID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	for _, id := range unassigned {
		p := project.NewDetachSSHKeyFromClusterParams()
		p.SetProjectID(projectID)
		p.SetDC(seedDC)
		p.SetClusterID(clusterID)
		p.SetKeyID(id)
		_, err := k.client.Project.DetachSSHKeyFromCluster(p, k.auth)
		if err != nil {
			if e, ok := err.(*project.DetachSSHKeyFromClusterDefault); ok && e.Code() == http.StatusNotFound {
				continue
			}
			return diag.FromErr(err)
		}
	}

	if err := assignSSHKeysToCluster(projectID, seedDC, clusterID, assign, k); err != nil {
		return err
	}

	return nil
}

func assignSSHKeysToCluster(projectID, seedDC, clusterID string, sshkeyIDs []string, k *metakubeProviderMeta) diag.Diagnostics {
	for _, id := range sshkeyIDs {
		p := project.NewAssignSSHKeyToClusterParams()
		p.SetProjectID(projectID)
		p.SetDC(seedDC)
		p.SetClusterID(clusterID)
		p.SetKeyID(id)
		_, err := k.client.Project.AssignSSHKeyToCluster(p, k.auth)
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

func waitClusterReady(ctx context.Context, k *metakubeProviderMeta, d *schema.ResourceData, projectID, seedDC, clusterID string) error {
	return resource.RetryContext(ctx, d.Timeout(schema.TimeoutCreate), func() *resource.RetryError {

		p := project.NewGetClusterHealthParams()
		p.SetContext(ctx)
		p.SetProjectID(projectID)
		p.SetDC(seedDC)
		p.SetClusterID(clusterID)

		r, err := k.client.Project.GetClusterHealth(p, k.auth)
		if err != nil {
			return resource.RetryableError(fmt.Errorf("unable to get cluster '%s' health: %s", d.Id(), getErrorResponse(err)))
		}

		if r.Payload.Apiserver == healthStatusUp &&
			r.Payload.CloudProviderInfrastructure == healthStatusUp &&
			r.Payload.Controller == healthStatusUp &&
			r.Payload.Etcd == healthStatusUp &&
			r.Payload.MachineController == healthStatusUp &&
			r.Payload.Scheduler == healthStatusUp &&
			r.Payload.UserClusterControllerManager == healthStatusUp {
			return nil
		}

		k.log.Debugf("waiting for cluster '%s' to be ready, %+v", d.Id(), r.Payload)
		return resource.RetryableError(fmt.Errorf("waiting for cluster '%s' to be ready", d.Id()))
	})
}

func resourceClusterDelete(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	k := m.(*metakubeProviderMeta)

	projectID, seedDC, clusterID, err := metakubeClusterParseID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}
	p := project.NewDeleteClusterParams()

	p.SetProjectID(projectID)
	p.SetDC(seedDC)
	p.SetClusterID(clusterID)

	deleteSent := false
	err = resource.RetryContext(ctx, d.Timeout(schema.TimeoutDelete), func() *resource.RetryError {
		if !deleteSent {
			_, err := k.client.Project.DeleteCluster(p, k.auth)
			if err != nil {
				if e, ok := err.(*project.DeleteClusterDefault); ok {
					if e.Code() == http.StatusConflict {
						return resource.RetryableError(err)
					}
					if e.Code() == http.StatusNotFound {
						return nil
					}
				}
				if _, ok := err.(*project.DeleteClusterForbidden); ok {
					return nil
				}
				return resource.NonRetryableError(fmt.Errorf("unable to delete cluster '%s': %s", d.Id(), getErrorResponse(err)))
			}
			deleteSent = true
		}
		p := project.NewGetClusterParams()

		p.SetProjectID(projectID)
		p.SetDC(seedDC)
		p.SetClusterID(clusterID)

		r, err := k.client.Project.GetCluster(p, k.auth)
		if err != nil {
			if e, ok := err.(*project.GetClusterDefault); ok && e.Code() == http.StatusNotFound {
				k.log.Debugf("cluster '%s' has been destroyed, returned http code: %d", d.Id(), e.Code())
				return nil
			}
			if _, ok := err.(*project.GetClusterForbidden); ok {
				return nil
			}
			return resource.NonRetryableError(fmt.Errorf("unable to get cluster '%s': %s", d.Id(), getErrorResponse(err)))
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
