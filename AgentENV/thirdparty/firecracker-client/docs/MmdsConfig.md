# MmdsConfig

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**version** | Option<**Version**> | Enumeration indicating the MMDS version to be configured. (enum: V1, V2) | [optional][default to V1]
**network_interfaces** | **Vec<String>** | List of the network interface IDs capable of forwarding packets to the MMDS. Network interface IDs mentioned must be valid at the time of this request. The net device model will reply to HTTP GET requests sent to the MMDS address via the interfaces mentioned. In this case, both ARP requests and TCP segments heading to `ipv4_address` are intercepted by the device model, and do not reach the associated TAP device. | 
**ipv4_address** | Option<**String**> | A valid IPv4 link-local address. | [optional][default to 169.254.169.254]
**imds_compat** | Option<**bool**> | MMDS operates compatibly with EC2 IMDS (i.e. responds \"text/plain\" content regardless of Accept header in requests). | [optional][default to false]

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


