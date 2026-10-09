# Balloon

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**amount_mib** | **i32** | Target balloon size in MiB. | 
**deflate_on_oom** | **bool** | Whether the balloon should deflate when the guest has memory pressure. | 
**stats_polling_interval_s** | Option<**i32**> | Interval in seconds between refreshing statistics. A non-zero value will enable the statistics. Defaults to 0. | [optional]
**free_page_hinting** | Option<**bool**> | Whether the free page hinting feature is enabled. | [optional]
**free_page_reporting** | Option<**bool**> | Whether the free page reporting feature is enabled. | [optional]

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


