# FullVmConfiguration

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**balloon** | Option<[**models::Balloon**](Balloon.md)> |  | [optional]
**drives** | Option<[**Vec<models::Drive>**](Drive.md)> | Configurations for all block devices. | [optional]
**boot_source** | Option<[**models::BootSource**](BootSource.md)> |  | [optional]
**cpu_config** | Option<[**models::CpuConfig**](CpuConfig.md)> |  | [optional]
**logger** | Option<[**models::Logger**](Logger.md)> |  | [optional]
**machine_config** | Option<[**models::MachineConfiguration**](MachineConfiguration.md)> |  | [optional]
**metrics** | Option<[**models::Metrics**](Metrics.md)> |  | [optional]
**memory_hotplug** | Option<[**models::MemoryHotplugConfig**](MemoryHotplugConfig.md)> |  | [optional]
**mmds_config** | Option<[**models::MmdsConfig**](MmdsConfig.md)> |  | [optional]
**network_interfaces** | Option<[**Vec<models::NetworkInterface>**](NetworkInterface.md)> | Configurations for all net devices. | [optional]
**pmem** | Option<[**Vec<models::Pmem>**](Pmem.md)> | Configurations for all pmem devices. | [optional]
**vsock** | Option<[**models::Vsock**](Vsock.md)> |  | [optional]
**entropy** | Option<[**models::EntropyDevice**](EntropyDevice.md)> |  | [optional]

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


