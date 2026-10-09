# MemoryHotplugConfig

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**total_size_mib** | Option<**i32**> | Total size of the hotpluggable memory in MiB. | [optional]
**slot_size_mib** | Option<**i32**> | Slot size for the hotpluggable memory in MiB. This will determine the granularity of hot-plug memory from the host. Refer to the device documentation on how to tune this value. | [optional]
**block_size_mib** | Option<**i32**> | (Logical) Block size for the hotpluggable memory in MiB. This will determine the logical granularity of hot-plug memory for the guest. Refer to the device documentation on how to tune this value. | [optional]

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


