# MsrModifier

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**addr** | **String** | 32-bit MSR address as hex, binary, or decimal string (e.g., \"0x10a\", \"0b100001010\", \"266\") | 
**bitmap** | **String** | 64-bit bitmap string defining which bits to modify. Format is \"0b\" followed by 64 characters where '0' = clear bit, '1' = set bit, 'x' = don't modify. Underscores can be used for readability. Example \"0b0000000000000000000000000000000000000000000000000000000000000001\" | 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


