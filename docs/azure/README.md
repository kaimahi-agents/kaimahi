# Guidance for Azure

Choose a sample for the inference backend you want to run:

| Sample | Model hosting | Workflow |
|---|---|---|
| [CPU-model agent](cpu-model/README.md) | The same small self-hosted model on local kind and AKS | Bicep setup, local evaluation, ACR publication and AKS Lift |

For hosted inference, see [Foundry configuration](../local-foundry-inference.md)
and the existing [AKS walkthrough](../aks-demo.md). These remain separate from
the CPU-model sample; their prerequisites and model costs differ.
