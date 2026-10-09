# CpuConfig

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**kvm_capabilities** | Option<**Vec<String>**> | A collection of KVM capabilities to be added or removed (both x86_64 and aarch64) | [optional]
**cpuid_modifiers** | Option<[**Vec<models::CpuidLeafModifier>**](CpuidLeafModifier.md)> | A collection of CPUID leaf modifiers (x86_64 only) | [optional]
**msr_modifiers** | Option<[**Vec<models::MsrModifier>**](MsrModifier.md)> | A collection of model specific register modifiers (x86_64 only) | [optional]
**reg_modifiers** | Option<[**Vec<models::ArmRegisterModifier>**](ArmRegisterModifier.md)> | A collection of register modifiers (aarch64 only) | [optional]
**vcpu_features** | Option<[**Vec<models::VcpuFeatures>**](VcpuFeatures.md)> | A collection of vCPU features to be modified (aarch64 only) | [optional]

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


