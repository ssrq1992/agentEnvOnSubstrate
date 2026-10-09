# SnapshotCreateParams

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**mem_file_path** | Option<**String**> | Path to the file that will contain the guest memory. If not specified, only the VM state is saved without memory. | [optional]
**snapshot_path** | **String** | Path to the file that will contain the microVM state. | 
**snapshot_type** | Option<**SnapshotType**> | Type of snapshot to create. It is optional and by default, a full snapshot is created. (enum: Full, Diff) | [optional]

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


