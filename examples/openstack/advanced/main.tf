terraform {
  required_providers {
    metakube = {
      source = "syseleven/metakube"
    }
    openstack = {
      source = "terraform-provider-openstack/openstack"
    }
  }
}

resource "openstack_networking_secgroup_v2" "cluster-net" {
  name = var.server_group_name
}

resource "openstack_networking_secgroup_rule_v2" "allow_ipv4_within_group" {
  direction = "ingress"
  ethertype = "IPv4"
  remote_group_id = openstack_networking_secgroup_v2.cluster-net.id
  security_group_id = openstack_networking_secgroup_v2.cluster-net.id
}

resource "openstack_networking_secgroup_rule_v2" "allow_ipv6_within_group" {
  direction = "ingress"
  ethertype = "IPv6"
  remote_group_id = openstack_networking_secgroup_v2.cluster-net.id
  security_group_id = openstack_networking_secgroup_v2.cluster-net.id
}

resource "openstack_networking_secgroup_rule_v2" "allow_ssh" {
  direction = "ingress"
  ethertype = "IPv4"
  port_range_min = 22
  port_range_max = 22
  protocol = "tcp"
  security_group_id = openstack_networking_secgroup_v2.cluster-net.id
}

resource "openstack_networking_secgroup_rule_v2" "allow_icmp" {
  direction = "ingress"
  ethertype = "IPv4"
  protocol = "icmp"
  security_group_id = openstack_networking_secgroup_v2.cluster-net.id
}

resource "openstack_networking_secgroup_rule_v2" "allow_icmp6" {
  direction = "ingress"
  ethertype = "IPv6"
  protocol = "ipv6-icmp"
  security_group_id = openstack_networking_secgroup_v2.cluster-net.id
}

resource "openstack_networking_secgroup_rule_v2" "allow_higher_ports" {
  direction = "ingress"
  ethertype = "IPv4"
  protocol = "tcp"
  security_group_id = openstack_networking_secgroup_v2.cluster-net.id
  port_range_min = 30000
  port_range_max = 32767
  remote_ip_prefix = "192.168.1.0/24"
}

resource "openstack_networking_network_v2" "network_1" {
  name = var.cluster_network_name
  admin_state_up = true
}

resource "openstack_networking_subnet_v2" "subnet_1" {
  name = var.subnet_name
  network_id = openstack_networking_network_v2.network_1.id
  cidr = "192.168.1.0/24"
  ip_version = 4
  enable_dhcp = true
  allocation_pool {
    start = "192.168.1.2"
    end = "192.168.1.254"
  }
  dns_nameservers = [
    "37.123.105.116",
    "37.123.105.117"]
}

data "openstack_networking_network_v2" "external" {
  name = var.floating_ip_pool
}

resource "openstack_networking_router_v2" "router_1" {
  name = var.router_name
  admin_state_up = true
  external_network_id = data.openstack_networking_network_v2.external.id
}

resource "openstack_networking_router_interface_v2" "router_interface_1" {
  router_id = openstack_networking_router_v2.router_1.id
  subnet_id = openstack_networking_subnet_v2.subnet_1.id
}


data openstack_images_image_v2 "image" {
  most_recent = true

  visibility = "public"
  properties = {
    os_distro = "ubuntu"
    os_version = "18.04"
  }
}

resource "metakube_project" "project" {
  name = var.project_name
  labels = {
    "foo" = "bar"
  }

  // You can add as many collaborators as you want.
//    user {
//      email = "FILL_IN"
//      group = "owners" // editors, viewers
//    }
}

data "local_file" "public_sshkey" {
  filename = pathexpand(var.public_sshkey_file)
}

resource "metakube_sshkey" "local" {
  project_id = metakube_project.project.id

  name = "local SSH key"
  public_key = data.local_file.public_sshkey.content
}

resource "metakube_cluster" "cluster" {
  name = var.cluster_name
  dc_name = var.dc_name
  project_id = metakube_project.project.id

  sshkeys = [metakube_sshkey.local.id]


  type = "kubernetes"
  # should not introduce any change hence type should be computed to this value anyway

  # add labels
  labels = {
    "test-key" = "test-value"
  }

  spec {
    version = var.k8s_version
    cloud {
      openstack {
        tenant = var.tenant
        username = var.username
        password = var.password
        floating_ip_pool = data.openstack_networking_network_v2.external.name
        security_group = openstack_networking_secgroup_v2.cluster-net.name
        network = openstack_networking_network_v2.network_1.name
        subnet_id = openstack_networking_subnet_v2.subnet_1.id
        subnet_cidr = openstack_networking_secgroup_rule_v2.allow_higher_ports.remote_ip_prefix
      }
    }

    # enable audit logging
    audit_logging = true
    pod_node_selector = true
    pod_security_policy = true
    domain_name = var.cluster_domain
    services_cidr = "10.240.16.0/20"
    pods_cidr = "172.25.0.0/16"
  }
}

# create admin.conf file
resource "local_file" "kubeconfig" {
  content     = metakube_cluster.cluster.kube_config
  filename = "${path.module}/admin.conf"
}

resource "metakube_node_deployment" "acctest_nd" {
  cluster_id = metakube_cluster.cluster.id
  name = null // auto generate

  spec {
    replicas = var.node_replicas
    min_replicas = var.node_min_replicas
    max_replicas = var.node_max_replicas

    dynamic_config = true

    template {
      labels = {
        key = "value"
      }

      cloud {
        openstack {
          flavor = var.node_flavor
          disk_size = var.node_disk_size
          image           = var.node_image != null ? var.node_image : data.openstack_images_image_v2.image.name
          use_floating_ip = var.use_floating_ip
          tags = {
            foo = "bar"
          }
        }
      }
      operating_system {
        ubuntu {
          dist_upgrade_on_boot = true
        }
      }
      versions {
        kubelet = var.k8s_version
      }
    }
  }
}