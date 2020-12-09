package metakube

import (
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

func clusterSpecFields() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"version": {
			Type:        schema.TypeString,
			Required:    true,
			Description: "Cloud orchestrator version, either Kubernetes or OpenShift",
		},
		"cloud": {
			Type:        schema.TypeList,
			Required:    true,
			ForceNew:    true,
			MinItems:    1,
			MaxItems:    1,
			Description: "Cloud provider specification",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"bringyourown": {
						Type:          schema.TypeList,
						Optional:      true,
						MaxItems:      1,
						Elem:          &schema.Resource{},
						Description:   "Bring your own infrastructure",
						ConflictsWith: []string{"spec.0.cloud.0.aws", "spec.0.cloud.0.openstack", "spec.0.cloud.0.azure"},
					},
					"aws": {
						Type:        schema.TypeList,
						Optional:    true,
						MaxItems:    1,
						Description: "AWS cluster specification",
						Elem: &schema.Resource{
							Schema: awsCloudSpecFields(),
						},
						ConflictsWith: []string{"spec.0.cloud.0.bringyourown", "spec.0.cloud.0.openstack", "spec.0.cloud.0.azure"},
					},
					"openstack": {
						Type:        schema.TypeList,
						Optional:    true,
						MaxItems:    1,
						Description: "OpenStack cluster specification",
						Elem: &schema.Resource{
							Schema: openstackCloudSpecFields(),
						},
						ConflictsWith: []string{"spec.0.cloud.0.aws", "spec.0.cloud.0.bringyourown", "spec.0.cloud.0.azure"},
					},
					"azure": {
						Type:        schema.TypeList,
						Optional:    true,
						ForceNew:    true,
						Description: "Azire cluster specification",
						Elem: &schema.Resource{
							Schema: azureCloudSpecFields(),
						},
						ConflictsWith: []string{"spec.0.cloud.0.aws", "spec.0.cloud.0.openstack", "spec.0.cloud.0.bringyourown"},
					},
				},
			},
		},
		"machine_networks": {
			Type:        schema.TypeList,
			Optional:    true,
			ForceNew:    true,
			Description: "Machine networks optionally specifies the parameters for IPAM",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"cidr": {
						Type:        schema.TypeString,
						Optional:    true,
						Description: "Network CIDR",
					},
					"gateway": {
						Type:        schema.TypeString,
						Optional:    true,
						Description: "Network gateway",
					},
					"dns_servers": {
						Type:        schema.TypeSet,
						Optional:    true,
						Description: "DNS servers",
						Elem:        schema.TypeString,
					},
				},
			},
		},
		"audit_logging": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     false,
			Description: "Whether to enable audit logging or not",
		},
		"pod_security_policy": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     false,
			Description: "Pod security policies allow detailed authorization of pod creation and updates",
		},
		"pod_node_selector": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     false,
			Description: "Configure PodNodeSelector admission plugin at the apiserver",
		},
		"services_cidr": {
			Type:        schema.TypeString,
			Optional:    true,
			ForceNew:    true,
			Computed:    true,
			Description: "Internal IP range for ClusterIP Services",
		},
		"pods_cidr": {
			Type:        schema.TypeString,
			Optional:    true,
			ForceNew:    true,
			Computed:    true,
			Description: "Internal IP range for Pods",
		},
		"domain_name": {
			Type:        schema.TypeString,
			Optional:    true,
			ForceNew:    true,
			Computed:    true,
			Description: "Internal IP range for ClusterIP Pods",
		},
	}
}

func azureCloudSpecFields() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"availability_set": {
			Type:     schema.TypeString,
			Optional: true,
		},
		"client_id": {
			Type:     schema.TypeString,
			Required: true,
		},
		"client_secret": {
			Type:      schema.TypeString,
			Required:  true,
			Sensitive: true,
		},
		"subscription_id": {
			Type:     schema.TypeString,
			Required: true,
		},
		"tenant_id": {
			Type:     schema.TypeString,
			Required: true,
		},
		"resource_group": {
			Type:     schema.TypeString,
			Computed: true,
			Optional: true,
		},
		"route_table": {
			Type:     schema.TypeString,
			Computed: true,
			Optional: true,
		},
		"security_group": {
			Type:     schema.TypeString,
			Computed: true,
			Optional: true,
		},
		"subnet": {
			Type:     schema.TypeString,
			Computed: true,
			Optional: true,
		},
		"vnet": {
			Type:     schema.TypeString,
			Computed: true,
			Optional: true,
		},
	}
}

func awsCloudSpecFields() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"access_key_id": {
			Type:        schema.TypeString,
			Required:    true,
			Sensitive:   true,
			Description: "Access key identifier",
		},
		"secret_access_key": {
			Type:        schema.TypeString,
			Required:    true,
			Sensitive:   true,
			Description: "Secret access key",
		},
		"vpc_id": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "Virtual private cloud identifier",
		},
		"security_group_id": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "Security group identifier",
		},
		"route_table_id": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "Route table identifier",
		},
		"instance_profile_name": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "Instance profile name",
		},
		"role_arn": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "The IAM role the control plane will use over assume-role",
		},
	}
}

func openstackCloudSpecFields() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"tenant": {
			Type:         schema.TypeString,
			Required:     true,
			ForceNew:     true,
			DefaultFunc:  schema.EnvDefaultFunc("OS_PROJECT_NAME", ""),
			ValidateFunc: validation.NoZeroValues,
			Description:  "The opestack project to use for billing",
		},
		"username": {
			Type:         schema.TypeString,
			DefaultFunc:  schema.EnvDefaultFunc("OS_USERNAME", ""),
			Required:     true,
			Sensitive:    true,
			ValidateFunc: validation.NoZeroValues,
			Description:  "The openstack account's username",
		},
		"password": {
			Type:         schema.TypeString,
			DefaultFunc:  schema.EnvDefaultFunc("OS_PASSWORD", ""),
			Required:     true,
			Sensitive:    true,
			ValidateFunc: validation.NoZeroValues,
			Description:  "The openstack account's password",
		},
		"floating_ip_pool": {
			Type:        schema.TypeString,
			Computed:    true,
			Optional:    true,
			ForceNew:    true,
			Description: "The floating ip pool used by all worker nodes to receive a public ip",
		},
		"security_group": {
			Type:        schema.TypeString,
			Computed:    true,
			Optional:    true,
			ForceNew:    true,
			Description: "When specified, all worker nodes will be attached to this security group. If not specified, a security group will be created",
		},
		"network": {
			Type:        schema.TypeString,
			Computed:    true,
			Optional:    true,
			ForceNew:    true,
			Description: "When specified, all worker nodes will be attached to this network. If not specified, a network, subnet & router will be created.",
		},
		"subnet_id": {
			Type:         schema.TypeString,
			Computed:     true,
			Optional:     true,
			ForceNew:     true,
			RequiredWith: []string{"spec.0.cloud.0.openstack.0.network"},
			Description:  "When specified, all worker nodes will be attached to this subnet of specified network. If not specified, a network, subnet & router will be created.",
		},
		"subnet_cidr": {
			Type:         schema.TypeString,
			Computed:     true,
			Optional:     true,
			ForceNew:     true,
			RequiredWith: []string{"spec.0.cloud.0.openstack.0.network", "spec.0.cloud.0.openstack.0.subnet_id"},
			Description:  "Change this to configure a different internal IP range for Nodes. Default: 192.168.1.0/24",
		},
	}
}

func kubernetesConfigSchema() *schema.Schema {
	return &schema.Schema{
		Type:      schema.TypeList,
		MaxItems:  1,
		Computed:  true,
		Sensitive: true,
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"raw_config": {
					Type:     schema.TypeString,
					Computed: true,
				},
			},
		},
	}
}
