# CpuidLeafModifier

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**leaf** | **String** | CPUID leaf index as hex, binary, or decimal string (e.g., \"0x0\", \"0b0\", \"0\")) | 
**subleaf** | **String** | CPUID subleaf index as hex, binary, or decimal string (e.g., \"0x0\", \"0b0\", \"0\") | 
**flags** | **i32** | KVM feature flags for this leaf-subleaf | 
**modifiers** | [**Vec<models::CpuidRegisterModifier>**](CpuidRegisterModifier.md) | Register modifiers for this CPUID leaf | 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


