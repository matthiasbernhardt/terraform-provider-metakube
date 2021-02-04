package metakube

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/syseleven/terraform-provider-metakube/go-metakube/client/openstack"
	"github.com/syseleven/terraform-provider-metakube/go-metakube/client/versions"
	"github.com/syseleven/terraform-provider-metakube/go-metakube/models"
)

type openstackValidationData struct {
	dcName      *string
	credentials *string
	domain      *string
	username    *string
	password    *string
	tenant      *string
	network     *string
	subnetID    *string
}

type generalOpenstackReqParams interface {
	SetDatacenterName(*string)
	SetCredential(*string)
	SetDomain(*string)
	SetUsername(*string)
	SetPassword(*string)
	SetTenant(*string)
	SetContext(context.Context)
}

func (data *openstackValidationData) setParams(ctx context.Context, p generalOpenstackReqParams) {
	p.SetDatacenterName(data.dcName)
	p.SetCredential(data.credentials)
	p.SetDomain(data.domain)
	p.SetUsername(data.username)
	p.SetPassword(data.password)
	p.SetTenant(data.tenant)
	p.SetContext(ctx)
}

func newOpenstackValidationData(d *schema.ResourceData) openstackValidationData {
	return openstackValidationData{
		dcName:      toStrPtrOrNil(d.Get("dc_name")),
		credentials: toStrPtrOrNil(d.Get("credential")),
		domain:      strToPtr("Default"),
		username:    toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.username")),
		password:    toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.password")),
		tenant:      toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.tenant")),
		network:     toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.network")),
		subnetID:    toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.subnet_id")),
	}
}

func toStrPtrOrNil(v interface{}) *string {
	if v == nil {
		return nil
	}
	return strToPtr(v.(string))
}

func validateClusterFields(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
	ret := validateVersionExists(ctx, d, k)
	ret = append(ret, diagnoseOpenstackFloatingIPPoolIfSet(ctx, d, k)...)
	ret = append(ret, diagnoseOpenstackNetworkExistsIfSet(ctx, d, k)...)
	return append(ret, diagnoseOpenstackSubnetWithIDExistsIfSet(ctx, d, k)...)
}

func validateVersionExists(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
	version := d.Get("spec.0.version").(string)
	p := versions.NewGetMasterVersionsParams().WithContext(ctx)
	r, err := k.client.Versions.GetMasterVersions(p, k.auth)
	if err != nil {
		diag.Errorf("%s", getErrorResponse(err))
	}

	available := make([]string, 0)
	for _, v := range r.Payload {
		available = append(available, v.Version.(string))
		if s, ok := v.Version.(string); ok && s == version {
			return nil
		}
	}

	return diag.Diagnostics{{
		Severity:      diag.Error,
		Summary:       fmt.Sprintf("unknown version %s", version),
		AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("version"),
		Detail:        fmt.Sprintf("Please select one of available versions: %v", available),
	}}
}

func diagnoseOpenstackFloatingIPPoolIfSet(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
	nets, err := validateOpenstackNetworkExistsIfSet(ctx, d, k, "spec.0.cloud.0.openstack.0.floating_ip_pool", true)
	names := make([]string, 0)
	for _, n := range nets {
		if n.External {
			names = append(names, n.Name)
		}
	}
	var diagnoseDetail string
	if len(names) > 0 {
		diagnoseDetail = fmt.Sprintf("We found following floating IP pools: %v", names)
	}
	if err != nil {
		return diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       err.Error(),
			AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("cloud").IndexInt(0).GetAttr("openstack").IndexInt(0).GetAttr("floating_ip_pool"),
			Detail:        diagnoseDetail,
		}}
	}
	return nil
}

func diagnoseOpenstackNetworkExistsIfSet(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
	allnets, err := validateOpenstackNetworkExistsIfSet(ctx, d, k, "spec.0.cloud.0.openstack.0.network", false)
	if err != nil {
		names := make([]string, 0)
		for _, n := range allnets {
			if n.External == false {
				names = append(names, n.Name)
			}
		}
		var diagnoseDetail string
		if len(names) > 0 {
			diagnoseDetail = fmt.Sprintf("We found following networks: %v", names)
		}
		return diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       err.Error(),
			AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("cloud").IndexInt(0).GetAttr("openstack").IndexInt(0).GetAttr("network"),
			Detail:        diagnoseDetail,
		}}
	}
	return nil
}

func validateOpenstackNetworkExistsIfSet(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta, field string, external bool) ([]*models.OpenstackNetwork, error) {
	value, ok := d.GetOk(field)
	if !ok {
		return nil, nil
	}

	data := newOpenstackValidationData(d)
	_, all, err := getNetwork(ctx, k, data, value.(string), external)
	return all, err
}

func diagnoseOpenstackSubnetWithIDExistsIfSet(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
	data := newOpenstackValidationData(d)
	if data.network == nil || data.subnetID == nil {
		return nil
	}
	network, _, err := getNetwork(ctx, k, data, *data.network, true)
	if err != nil {
		return nil
	}

	subnets, ok, err := getSubnet(ctx, k, data, network.ID)
	if ok {
		return nil
	}
	var diagnoseDetail string
	if len(subnets) > 0 {
		tmp := make([]string, 0)
		for _, i := range subnets {
			tmp = append(tmp, fmt.Sprintf("%s/%s", i.Name, i.ID))
		}
		diagnoseDetail = fmt.Sprintf("We found following subnets (name/id): %v", tmp)
	}
	return diag.Diagnostics{{
		Severity:      diag.Error,
		Summary:       err.Error(),
		AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("cloud").IndexInt(0).GetAttr("openstack").IndexInt(0).GetAttr("subnetID"),
		Detail:        diagnoseDetail,
	}}
}

func getNetwork(ctx context.Context, k *metakubeProviderMeta, data openstackValidationData, name string, external bool) (*models.OpenstackNetwork, []*models.OpenstackNetwork, error) {
	p := openstack.NewListOpenstackNetworksParams()
	data.setParams(ctx, p)
	res, err := k.client.Openstack.ListOpenstackNetworks(p, k.auth)
	if err != nil {
		return nil, nil, fmt.Errorf("find network instance %v", getErrorResponse(err))
	}
	ret := findNetwork(res.Payload, name, external)
	if ret == nil {
		return nil, res.Payload, fmt.Errorf("network `%s` not found", name)
	}
	return ret, res.Payload, nil
}

func findNetwork(list []*models.OpenstackNetwork, network string, external bool) *models.OpenstackNetwork {
	for _, item := range list {
		if item.Name == network && item.External == external {
			return item
		}
	}
	return nil
}

func getSubnet(ctx context.Context, k *metakubeProviderMeta, data openstackValidationData, networkID string) ([]*models.OpenstackSubnet, bool, error) {
	p := openstack.NewListOpenstackSubnetsParams()
	data.setParams(ctx, p)
	p.SetNetworkID(&networkID)
	res, err := k.client.Openstack.ListOpenstackSubnets(p, k.auth)
	if err != nil {
		return nil, false, fmt.Errorf("list network subnets: %v", getErrorResponse(err))
	}
	return res.Payload, findSubnet(res.Payload, *data.subnetID) != nil, nil
}

func findSubnet(list []*models.OpenstackSubnet, id string) *models.OpenstackSubnet {
	for _, item := range list {
		if item.ID == id {
			return item
		}
	}
	return nil
}
