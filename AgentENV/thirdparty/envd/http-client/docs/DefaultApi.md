# \DefaultApi

All URIs are relative to *http://localhost*

Method | HTTP request | Description
------------- | ------------- | -------------
[**envs_get**](DefaultApi.md#envs_get) | **GET** /envs | Get the environment variables
[**health_get**](DefaultApi.md#health_get) | **GET** /health | Check the health of the service
[**init_post**](DefaultApi.md#init_post) | **POST** /init | Set initial vars, ensure the time and metadata is synced with the host
[**metrics_get**](DefaultApi.md#metrics_get) | **GET** /metrics | Get the stats of the service



## envs_get

> std::collections::HashMap<String, String> envs_get()
Get the environment variables

### Parameters

This endpoint does not need any parameter.

### Return type

**std::collections::HashMap<String, String>**

### Authorization

[AccessTokenAuth](../README.md#AccessTokenAuth)

### HTTP request headers

- **Content-Type**: Not defined
- **Accept**: application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)


## health_get

> health_get()
Check the health of the service

### Parameters

This endpoint does not need any parameter.

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

- **Content-Type**: Not defined
- **Accept**: Not defined

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)


## init_post

> init_post(init_post_request)
Set initial vars, ensure the time and metadata is synced with the host

### Parameters


Name | Type | Description  | Required | Notes
------------- | ------------- | ------------- | ------------- | -------------
**init_post_request** | Option<[**InitPostRequest**](InitPostRequest.md)> |  |  |

### Return type

 (empty response body)

### Authorization

[AccessTokenAuth](../README.md#AccessTokenAuth)

### HTTP request headers

- **Content-Type**: application/json
- **Accept**: Not defined

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)


## metrics_get

> models::Metrics metrics_get()
Get the stats of the service

### Parameters

This endpoint does not need any parameter.

### Return type

[**models::Metrics**](Metrics.md)

### Authorization

[AccessTokenAuth](../README.md#AccessTokenAuth)

### HTTP request headers

- **Content-Type**: Not defined
- **Accept**: application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

