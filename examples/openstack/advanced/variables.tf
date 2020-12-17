variable "server_group_name" {
  description = "Openstack server group name"
  type = string
  default = "server_group_1"
}
variable "cluster_network_name" {
  description = "Openstack cluster network name"
  type = string
  default = "network_1"
}
variable "subnet_name" {
  description = "Openstack subnet name"
  type = string
  default = "subnet_1"
}
variable "router_name" {
  description = "Openstack router name"
  type = string
  default = "router_1"
}
variable project_name {
  description = "Name of the MetaKube project"
  type        = string
}
variable "public_sshkey_file" {
  description = "Path to public ssh key to add to cluster"
  type = string
  default = "~/.ssh/id_rsa.pub"
}
variable cluster_name {
  description = "Name of the MetaKube cluster"
  type        = string
}
variable "cluster_domain" {
  description = "MetaKube Cluster domain"
  type = string
  default = "cluster.local"
}

variable k8s_version {
  description = "Kubernetes version"
  type        = string
}

variable dc_name {
  description = "Datacenter where the cluster should be deployed to. Get a list of possible option with `curl -s -H \"authorization: Bearer $METAKUBE_TOKEN\" https://metakube.syseleven.de/api/v1/dc | jq -r '.[] | select(.seed!=true) | .metadata.name'`"
  type        = string
  default     = "syseleven-dbl1"
}

variable floating_ip_pool {
  description = "Floating IP pool to use for all worker nodes"
  type        = string
  default     = "ext-net"
}

variable username {
  description = "OpenStack username"
  type        = string
  default = null
}

variable password {
  description = "OpenStack password"
  type        = string
  default = null
}

variable tenant {
  description = "OpenStack tenant"
  type        = string
  default = null
}

variable node_flavor {
  description = "Flavor of the k8s worker node"
  type        = string
  default     = "m1.medium"
}

variable node_image {
  description = "Name of the image which should be used for worker nodes"
  type        = string
  default     = null
}

variable node_replicas {
  description = "Amount of worker nodes in this node deployment"
  type        = string
  default     = 3
}

variable "node_max_replicas" {
  description = "Max number of replicas to autoscale to"
  type = number
  default = 3
}

variable "node_min_replicas" {
  description = "Min number of replicas to downscale to"
  type = number
  default = 3
}

variable "node_disk_size" {
  description = "Disk size for a node"
  type = number
  default = null
}

variable use_floating_ip {
  description = "If the node deployment should use floating IPs"
  type        = bool
  default     = true
}
