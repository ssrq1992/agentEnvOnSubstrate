# \FilesApi

All URIs are relative to *http://localhost*

Method | HTTP request | Description
------------- | ------------- | -------------
[**files_compose_post**](FilesApi.md#files_compose_post) | **POST** /files/compose | Compose multiple files into a single file using zero-copy concatenation. Source files are deleted after successful composition.
[**files_get**](FilesApi.md#files_get) | **GET** /files | Download a file
[**files_post**](FilesApi.md#files_post) | **POST** /files | Upload a file and ensure the parent directories exist. If the file exists, it will be overwritten.



## files_compose_post

> models::EntryInfo files_compose_post(compose_request)
Compose multiple files into a single file using zero-copy concatenation. Source files are deleted after successful composition.

### Parameters


Name | Type | Description  | Required | Notes
------------- | ------------- | ------------- | ------------- | -------------
**compose_request** | [**ComposeRequest**](ComposeRequest.md) |  | [required] |

### Return type

[**models::EntryInfo**](EntryInfo.md)

### Authorization

[AccessTokenAuth](../README.md#AccessTokenAuth)

### HTTP request headers

- **Content-Type**: application/json
- **Accept**: application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)


## files_get

> std::path::PathBuf files_get(path, username, signature, signature_expiration)
Download a file

### Parameters


Name | Type | Description  | Required | Notes
------------- | ------------- | ------------- | ------------- | -------------
**path** | Option<**String**> | Path to the file, URL encoded. Can be relative to the user's home directory (e.g. \"file.txt\" resolves to ~/file.txt). |  |
**username** | Option<**String**> | User for setting file ownership and resolving relative paths. Defaults to the sandbox's default user. |  |
**signature** | Option<**String**> | Signature used for file access permission verification. |  |
**signature_expiration** | Option<**i32**> | Unix timestamp (seconds) after which the signature expires. Only used with the signature parameter. |  |

### Return type

[**std::path::PathBuf**](std::path::PathBuf.md)

### Authorization

[AccessTokenAuth](../README.md#AccessTokenAuth)

### HTTP request headers

- **Content-Type**: Not defined
- **Accept**: application/octet-stream, application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)


## files_post

> Vec<models::EntryInfo> files_post(path, username, signature, signature_expiration, file)
Upload a file and ensure the parent directories exist. If the file exists, it will be overwritten.

### Parameters


Name | Type | Description  | Required | Notes
------------- | ------------- | ------------- | ------------- | -------------
**path** | Option<**String**> | Path to the file, URL encoded. Can be relative to the user's home directory (e.g. \"file.txt\" resolves to ~/file.txt). |  |
**username** | Option<**String**> | User for setting file ownership and resolving relative paths. Defaults to the sandbox's default user. |  |
**signature** | Option<**String**> | Signature used for file access permission verification. |  |
**signature_expiration** | Option<**i32**> | Unix timestamp (seconds) after which the signature expires. Only used with the signature parameter. |  |
**file** | Option<**std::path::PathBuf**> |  |  |

### Return type

[**Vec<models::EntryInfo>**](EntryInfo.md)

### Authorization

[AccessTokenAuth](../README.md#AccessTokenAuth)

### HTTP request headers

- **Content-Type**: multipart/form-data, application/octet-stream
- **Accept**: application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

