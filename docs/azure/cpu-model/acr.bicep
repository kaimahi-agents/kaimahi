targetScope = 'resourceGroup'

@description('Globally unique Azure Container Registry name for reviewed development suites.')
@minLength(5)
@maxLength(50)
param registryName string

@description('Azure region; defaults to the selected resource group location.')
param location string = resourceGroup().location

resource registry 'Microsoft.ContainerRegistry/registries@2025-04-01' = {
  name: registryName
  location: location
  sku: {
    name: 'Basic'
  }
  properties: {
    adminUserEnabled: false
    anonymousPullEnabled: false
    dataEndpointEnabled: false
    encryption: {
      status: 'disabled'
    }
    policies: {
      azureADAuthenticationAsArmPolicy: {
        status: 'enabled'
      }
    }
    publicNetworkAccess: 'Enabled'
  }
}

output registryId string = registry.id
output loginServer string = registry.properties.loginServer
