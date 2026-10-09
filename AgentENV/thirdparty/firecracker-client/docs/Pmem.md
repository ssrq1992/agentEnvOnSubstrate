# Pmem

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**id** | **String** | Identificator for this device. | 
**path_on_host** | **String** | Host level path for the virtio-pmem device to use as a backing file. | 
**root_device** | Option<**bool**> | Flag to make this device be the root device for VM boot. Setting this flag will fail if there is another device configured to be a root device already. | [optional]
**read_only** | Option<**bool**> | Flag to map backing file in read-only mode. | [optional]

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


