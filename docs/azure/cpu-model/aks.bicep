targetScope = 'resourceGroup'

@description('Name of a new operator-owned development cluster.')
@minLength(1)
param clusterName string

@description('Explicit region with the requested Kubernetes version and VM SKU available.')
param location string

@description('Exact supported Kubernetes version, selected using az aks get-versions.')
param kubernetesVersion string

@description('Reviewed public IPv4 CIDRs that can reach the API. Never use an empty list or 0.0.0.0/0.')
@minLength(1)
param apiServerAuthorizedIpRanges array

@description('Entra tenant containing the subscription; not a credential.')
param tenantId string

@description('New Azure-managed node resource group; do not place operator resources here.')
param nodeResourceGroup string

@description('Non-burstable Linux amd64 node size; verify regional capacity and quota before deployment.')
param nodeVMSize string = 'Standard_D4as_v5'

resource cluster 'Microsoft.ContainerService/managedClusters@2025-10-01' = {
  name: clusterName
  location: location
  sku: {
    name: 'Base'
    tier: 'Free'
  }
  identity: {
    type: 'SystemAssigned'
  }
  properties: {
    dnsPrefix: clusterName
    kubernetesVersion: kubernetesVersion
    nodeResourceGroup: nodeResourceGroup
    disableLocalAccounts: true
    aadProfile: {
      managed: true
      enableAzureRBAC: true
      tenantID: tenantId
    }
    apiServerAccessProfile: {
      enablePrivateCluster: false
      authorizedIPRanges: apiServerAuthorizedIpRanges
    }
    agentPoolProfiles: [
      {
        name: 'system'
        count: 1
        vmSize: nodeVMSize
        osType: 'Linux'
        osSKU: 'Ubuntu'
        osDiskType: 'Managed'
        osDiskSizeGB: 64
        mode: 'System'
        type: 'VirtualMachineScaleSets'
        enableAutoScaling: false
        maxPods: 250
        securityProfile: {
          sshAccess: 'Disabled'
        }
      }
    ]
    networkProfile: {
      networkPlugin: 'azure'
      networkPluginMode: 'overlay'
      networkDataplane: 'cilium'
      networkPolicy: 'cilium'
      podCidr: '10.244.0.0/16'
      serviceCidr: '10.0.0.0/16'
      dnsServiceIP: '10.0.0.10'
      loadBalancerSku: 'standard'
      outboundType: 'loadBalancer'
      loadBalancerProfile: {
        managedOutboundIPs: {
          count: 1
        }
      }
    }
    autoUpgradeProfile: {
      upgradeChannel: 'patch'
      nodeOSUpgradeChannel: 'NodeImage'
    }
    addonProfiles: {
      azurepolicy: {
        enabled: true
      }
    }
  }
}

output clusterId string = cluster.id
output nodeResourceGroupName string = cluster.properties.nodeResourceGroup
