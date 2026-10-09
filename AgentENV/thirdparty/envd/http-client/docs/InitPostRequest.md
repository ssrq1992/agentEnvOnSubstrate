# InitPostRequest

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**volume_mounts** | Option<[**Vec<models::VolumeMount>**](VolumeMount.md)> |  | [optional]
**hyperloop_ip** | Option<**String**> | IP address of the hyperloop server to connect to | [optional]
**env_vars** | Option<**std::collections::HashMap<String, String>**> | Environment variables to set | [optional]
**access_token** | Option<**String**> | Access token for secure access to envd service | [optional]
**timestamp** | Option<**chrono::DateTime<chrono::FixedOffset>**> | The current timestamp in RFC3339 format | [optional]
**default_user** | Option<**String**> | The default user to use for operations | [optional]
**default_workdir** | Option<**String**> | The default working directory to use for operations | [optional]
**ca_bundle** | Option<**String**> | PEM-encoded CA certificates to install into the system trust store (may contain multiple concatenated PEM blocks) | [optional]

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


