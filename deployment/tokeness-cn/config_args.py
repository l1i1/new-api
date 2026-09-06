#!/usr/bin/env python3
"""Serialize and verify Alibaba ESS ECI scaling configuration arguments.

The Aliyun ModifyEciScalingConfiguration API is a replace operation.  This
helper keeps the request schema explicit so a newly returned field cannot be
silently dropped.  Argument values are written as NUL-delimited records for a
caller that already has a private pipe to the process; diagnostics never
include configuration values.
"""

from __future__ import annotations

import argparse
import copy
import json
import re
import sys
from typing import Any, Iterable, NoReturn


class ConfigError(ValueError):
    """A configuration cannot be safely serialized or verified."""


# DescribeEciScalingConfigurations includes identity and lifecycle fields that
# are not accepted by ModifyEciScalingConfiguration.  They are safe to ignore
# because they are server-owned metadata, not desired configuration.
READ_ONLY_ROOT_FIELDS = {
    "ScalingConfigurationId",
    "ScalingGroupId",
    "LifecycleState",
    "CreationTime",
    "CreateTime",
    "ActiveTime",
    "InactiveTime",
    "ModificationTime",
    "ModifiedTime",
    "LastUpdateTime",
    "Status",
    "RequestId",
    # Describe-only echo fields: RegionId is implied by the API call, and
    # Description is not accepted by ModifyEciScalingConfiguration at all
    # (aliyun rejects the flag). Dropping them from the replacement is
    # lossless (readback ignores read-only fields anyway).
    "RegionId",
    "Description",
}

ROOT_SCALAR_FIELDS = {
    "ActiveDeadlineSeconds",
    "AutoCreateEip",
    "AutoMatchImageCache",
    "ContainerGroupName",
    "ContainersUpdateType",
    "CostOptimization",
    "Cpu",
    "CpuOptionsCore",
    "CpuOptionsThreadsPerCore",
    "DataCacheBucket",
    "DataCacheBurstingEnabled",
    "DataCachePL",
    "DataCacheProvisionedIops",
    "DnsPolicy",
    "EgressBandwidth",
    "EipBandwidth",
    "EphemeralStorage",
    "GpuDriverVersion",
    "HostName",
    "ImageSnapshotId",
    "IngressBandwidth",
    "InstanceFamilyLevel",
    "Ipv6AddressCount",
    "LoadBalancerWeight",
    "Memory",
    "Override",
    "RamRoleName",
    "ResourceGroupId",
    "RestartPolicy",
    "ScalingConfigurationName",
    "SecurityGroupId",
    "SpotPriceLimit",
    "SpotStrategy",
    "TerminationGracePeriodSeconds",
}

ROOT_REPEAT_FIELDS = {
    "DnsConfigNameServer",
    "DnsConfigSearch",
    "InstanceType",
    "NtpServer",
}

ROOT_OBJECT_LIST_FIELDS = {
    "AcrRegistryInfo",
    "HostAliase",
    "ImageRegistryCredential",
    "SecurityContextSysctl",
    "Tag",
    "Volume",
}

CONTAINER_SCALAR_FIELDS = {
    "Cpu",
    "Gpu",
    "Image",
    "ImagePullPolicy",
    "Memory",
    "Name",
    "Stdin",
    "StdinOnce",
    "Tty",
    "WorkingDir",
}

CONTAINER_REPEAT_FIELDS = {"Arg", "Command", "Port"}
CONTAINER_OBJECT_LIST_FIELDS = {"EnvironmentVar", "VolumeMount"}
CONTAINER_OBJECT_FIELDS = {
    "LivenessProbe",
    "ReadinessProbe",
    "SecurityContext",
    "LifecyclePostStartHandler",
    "LifecyclePreStopHandler",
}

PROBE_SCALAR_FIELDS = {
    "FailureThreshold",
    "InitialDelaySeconds",
    "PeriodSeconds",
    "SuccessThreshold",
    "TimeoutSeconds",
}

PROBE_OBJECT_FIELDS = {"Exec", "HttpGet", "TcpSocket"}

EXEC_FIELDS = {"Command"}
HTTP_GET_FIELDS = {"Path", "Port", "Scheme"}
TCP_SOCKET_FIELDS = {"Port"}

# DescribeEciScalingConfigurations returns env entries with the flat
# "FieldRefFieldPath" (secret reference) alongside Key/Value; the Modify API
# accepts the same flat spelling, so it round-trips unchanged.
ENV_FIELDS = {"Key", "Value", "FieldRefFieldPath"}
VOLUME_MOUNT_FIELDS = {"Name", "MountPath", "SubPath", "ReadOnly"}
SECURITY_CONTEXT_FIELDS = {"Capability", "ReadOnlyRootFilesystem", "RunAsUser"}
CAPABILITY_FIELDS = {"Add"}

LIFECYCLE_HANDLER_FIELDS = {"Exec", "HttpGet", "TcpSocket"}
LIFECYCLE_EXEC_FIELDS = {"Command"}
LIFECYCLE_HTTP_FIELDS = {"Host", "Path", "Port", "Scheme"}
LIFECYCLE_TCP_FIELDS = {"Host", "Port"}

DNS_CONFIG_FIELDS = {"NameServer", "Option", "Search"}
DNS_OPTION_FIELDS = {"Name", "Value"}
ACR_FIELDS = {"Domain", "InstanceId", "InstanceName", "RegionId"}
HOST_ALIAS_FIELDS = {"Hostname", "Ip"}
SYSCTL_FIELDS = {"Name", "Value"}
TAG_FIELDS = {"Key", "Value"}

# Volume has a common Name/Type plus one of the API's typed payloads.  Keeping
# this list explicit is what turns a newly populated unsupported field into a
# safe failure instead of a destructive partial update.
VOLUME_FIELDS = {
    "ConfigFileVolumeConfigFileToPath",
    "ConfigFileVolumeDefaultMode",
    "DiskVolume",
    "EmptyDirVolume",
    "FlexVolume",
    "HostPathVolume",
    "NFSVolume",
    "Name",
    "Type",
}
VOLUME_TYPED_FIELDS = {
    "ConfigFileVolumeConfigFileToPath": {"Key", "Path"},
    "DiskVolume": {"DiskId", "DiskSize", "FsType"},
    "EmptyDirVolume": {"Medium", "SizeLimit"},
    "FlexVolume": {"Driver", "FsType", "Options"},
    "HostPathVolume": {"Path", "Type"},
    "NFSVolume": {"Path", "ReadOnly", "Server"},
}

# InitContainer uses the same object schema but has distinct CLI prefixes for
# environment variables and volume mounts.
INIT_CONTAINER_SCALAR_FIELDS = {
    "Arg",
    "Command",
    "Cpu",
    "Gpu",
    "Image",
    "ImagePullPolicy",
    "Memory",
    "Name",
    "WorkingDir",
}

ALIASES = {
    "Containers": "Containers",
    "EnvironmentVars": "EnvironmentVars",
    "EnvironmentVar": "EnvironmentVar",
    "Args": "Arg",
    "Commands": "Command",
    "Ports": "Port",
    "VolumeMounts": "VolumeMount",
    "InitContainers": "InitContainers",
    "ImageRegistryCredentials": "ImageRegistryCredential",
    "InstanceTypes": "InstanceType",
    "NtpServers": "NtpServer",
    "HostAliases": "HostAliase",
    "SecurityContextSysctls": "SecurityContextSysctl",
    "Tags": "Tag",
    "Volumes": "Volume",
}


def fail(message: str) -> NoReturn:
    raise ConfigError(message)


def is_populated(value: Any) -> bool:
    """Return whether a field carries an explicit value worth preserving."""

    if value is None:
        return False
    if isinstance(value, (list, dict)) and not value:
        return False
    return True


def validate_scalar(path: str, value: Any) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        if isinstance(value, float) and (value != value or value in (float("inf"), float("-inf"))):
            fail(f"{path}: non-finite number")
        return str(value)
    if isinstance(value, str):
        if any(ord(char) < 0x20 or ord(char) == 0x7F for char in value):
            fail(f"{path}: control character")
        return value
    fail(f"{path}: unsupported value type")


def canonical_key(key: str) -> str:
    return ALIASES.get(key, key)


def reject_unknown(obj: dict[str, Any], allowed: set[str], path: str) -> None:
    for key, value in obj.items():
        key = canonical_key(key)
        if key not in allowed and is_populated(value):
            fail(f"{path}.{key}: unsupported populated field")


def normalize_probe(probe: Any, path: str) -> dict[str, Any]:
    if not isinstance(probe, dict):
        fail(f"{path}: expected object")
    normalized = {canonical_key(key): value for key, value in probe.items()}
    reject_unknown(normalized, PROBE_SCALAR_FIELDS | PROBE_OBJECT_FIELDS, path)
    for key in PROBE_OBJECT_FIELDS:
        value = normalized.get(key)
        if not is_populated(value):
            continue
        if not isinstance(value, dict):
            fail(f"{path}.{key}: expected object")
        if key == "Exec":
            reject_unknown(value, EXEC_FIELDS, f"{path}.{key}")
            command = value.get("Command")
            if is_populated(command) and not isinstance(command, list):
                fail(f"{path}.{key}.Command: expected list")
        elif key == "HttpGet":
            reject_unknown(value, HTTP_GET_FIELDS, f"{path}.{key}")
        else:
            reject_unknown(value, TCP_SOCKET_FIELDS, f"{path}.{key}")
    return normalized


def normalize_lifecycle_handler(value: Any, path: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        fail(f"{path}: expected object")
    normalized = {canonical_key(key): item for key, item in value.items()}
    reject_unknown(normalized, LIFECYCLE_HANDLER_FIELDS, path)
    for key in LIFECYCLE_HANDLER_FIELDS:
        item = normalized.get(key)
        if not is_populated(item):
            continue
        if not isinstance(item, dict):
            fail(f"{path}.{key}: expected object")
        if key == "Exec":
            reject_unknown(item, LIFECYCLE_EXEC_FIELDS, f"{path}.{key}")
            command = item.get("Command")
            if is_populated(command) and not isinstance(command, list):
                fail(f"{path}.{key}.Command: expected list")
        elif key == "HttpGet":
            reject_unknown(item, LIFECYCLE_HTTP_FIELDS, f"{path}.{key}")
        else:
            reject_unknown(item, LIFECYCLE_TCP_FIELDS, f"{path}.{key}")
    return normalized


def normalize_security_context(value: Any, path: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        fail(f"{path}: expected object")
    normalized = {canonical_key(key): item for key, item in value.items()}
    reject_unknown(normalized, SECURITY_CONTEXT_FIELDS, path)
    capability = normalized.get("Capability")
    if is_populated(capability):
        if not isinstance(capability, dict):
            fail(f"{path}.Capability: expected object")
        reject_unknown(capability, CAPABILITY_FIELDS, f"{path}.Capability")
        additions = capability.get("Add")
        if is_populated(additions) and not isinstance(additions, list):
            fail(f"{path}.Capability.Add: expected list")
    return normalized


def normalize_env_list(value: Any, path: str) -> list[dict[str, Any]]:
    if not isinstance(value, list):
        fail(f"{path}: expected list")
    result: list[dict[str, Any]] = []
    for index, item in enumerate(value, 1):
        if not isinstance(item, dict):
            fail(f"{path}[{index}]: expected object")
        normalized = {canonical_key(key): nested for key, nested in item.items()}
        reject_unknown(normalized, ENV_FIELDS, f"{path}[{index}]")
        if "Key" not in normalized or normalized.get("Key") is None:
            fail(f"{path}[{index}].Key: missing")
        if (
            ("Value" not in normalized or normalized.get("Value") is None)
            and ("FieldRefFieldPath" not in normalized or normalized.get("FieldRefFieldPath") is None)
        ):
            fail(f"{path}[{index}]: Value or FieldRefFieldPath missing")
        result.append(normalized)
    return result


def normalize_container(value: Any, path: str, init: bool = False) -> dict[str, Any]:
    if not isinstance(value, dict):
        fail(f"{path}: expected object")
    value = normalize_flat_probe_fields(value)
    normalized: dict[str, Any] = {}
    for key, item in value.items():
        normalized[canonical_key(key)] = item

    # DescribeEciScalingConfigurations returns the plural "EnvironmentVars"
    # key inside every container; the flat CLI arg is "EnvironmentVar".
    # Both spellings must normalize (serialize_container emits EnvironmentVar
    # from EnvironmentVars), or every real snapshot fails reject_unknown.
    allowed = (
        CONTAINER_SCALAR_FIELDS
        | CONTAINER_REPEAT_FIELDS
        | CONTAINER_OBJECT_LIST_FIELDS
        | {"EnvironmentVars"}
        | CONTAINER_OBJECT_FIELDS
    )
    if init:
        allowed = INIT_CONTAINER_SCALAR_FIELDS | {"EnvironmentVars", "VolumeMount"} | CONTAINER_OBJECT_FIELDS
    reject_unknown(normalized, allowed, path)

    for key in list(normalized):
        item = normalized[key]
        if not is_populated(item):
            continue
        item_path = f"{path}.{key}"
        if key in {"LivenessProbe", "ReadinessProbe"}:
            normalized[key] = normalize_probe(item, item_path)
        elif key == "SecurityContext":
            normalized[key] = normalize_security_context(item, item_path)
        elif key in {"LifecyclePostStartHandler", "LifecyclePreStopHandler"}:
            normalized[key] = normalize_lifecycle_handler(item, item_path)
        elif key == "EnvironmentVars":
            normalized[key] = normalize_env_list(item, item_path)
        elif key == "VolumeMount":
            if not isinstance(item, list):
                fail(f"{item_path}: expected list")
            mounts: list[dict[str, Any]] = []
            for index, mount in enumerate(item, 1):
                if not isinstance(mount, dict):
                    fail(f"{item_path}[{index}]: expected object")
                mount = {canonical_key(k): v for k, v in mount.items()}
                reject_unknown(mount, VOLUME_MOUNT_FIELDS, f"{item_path}[{index}]")
                mounts.append(mount)
            normalized[key] = mounts
        elif key in {"Arg", "Command", "Port"} and not isinstance(item, list):
            # Port can be a scalar repeat list in the API.  Arg/Command are
            # always lists; treating a scalar as a single item is unsafe for
            # those fields because it changes command semantics.
            if key != "Port":
                fail(f"{item_path}: expected list")
        elif key == "Port" and isinstance(item, list):
            normalized[key] = [normalize_port(port, f"{item_path}[{i}]") for i, port in enumerate(item, 1)]
    return normalized


def normalize_port(value: Any, path: str) -> Any:
    if not isinstance(value, dict):
        validate_scalar(path, value)
        return value
    # Port object fields are returned by some SDK versions; keep only fields
    # accepted by the repeat-list request shape.
    allowed = {"Port", "Protocol"}
    normalized = {canonical_key(key): item for key, item in value.items()}
    reject_unknown(normalized, allowed, path)
    return normalized


def normalize_dns(value: Any, path: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        fail(f"{path}: expected object")
    normalized = {canonical_key(key): item for key, item in value.items()}
    reject_unknown(normalized, DNS_CONFIG_FIELDS, path)
    options = normalized.get("Option")
    if is_populated(options):
        if not isinstance(options, list):
            fail(f"{path}.Option: expected list")
        normalized["Option"] = []
        for index, option in enumerate(options, 1):
            if not isinstance(option, dict):
                fail(f"{path}.Option[{index}]: expected object")
            option = {canonical_key(key): item for key, item in option.items()}
            reject_unknown(option, DNS_OPTION_FIELDS, f"{path}.Option[{index}]")
            normalized["Option"].append(option)
    for key in ("NameServer", "Search"):
        item = normalized.get(key)
        if is_populated(item) and not isinstance(item, list):
            fail(f"{path}.{key}: expected list")
    return normalized


def normalize_object_list(value: Any, fields: set[str], path: str) -> list[dict[str, Any]]:
    if not isinstance(value, list):
        fail(f"{path}: expected list")
    result: list[dict[str, Any]] = []
    for index, item in enumerate(value, 1):
        if not isinstance(item, dict):
            fail(f"{path}[{index}]: expected object")
        item = {canonical_key(key): nested for key, nested in item.items()}
        reject_unknown(item, fields, f"{path}[{index}]")
        result.append(item)
    return result


def normalize_volume(value: Any, path: str) -> list[dict[str, Any]]:
    if not isinstance(value, list):
        fail(f"{path}: expected list")
    result: list[dict[str, Any]] = []
    for index, item in enumerate(value, 1):
        if not isinstance(item, dict):
            fail(f"{path}[{index}]: expected object")
        item = {canonical_key(key): nested for key, nested in item.items()}
        reject_unknown(item, VOLUME_FIELDS, f"{path}[{index}]")
        for typed_key, typed_fields in VOLUME_TYPED_FIELDS.items():
            typed = item.get(typed_key)
            if not is_populated(typed):
                continue
            if typed_key == "ConfigFileVolumeConfigFileToPath":
                if not isinstance(typed, list):
                    fail(f"{path}[{index}].{typed_key}: expected list")
                item[typed_key] = normalize_object_list(typed, VOLUME_TYPED_FIELDS[typed_key], f"{path}[{index}].{typed_key}")
            else:
                if not isinstance(typed, dict):
                    fail(f"{path}[{index}].{typed_key}: expected object")
                item[typed_key] = {canonical_key(k): v for k, v in typed.items()}
                reject_unknown(item[typed_key], typed_fields, f"{path}[{index}].{typed_key}")
        result.append(item)
    return result


def normalize_flat_probe_fields(container: dict[str, Any]) -> dict[str, Any]:
    """Accept flattened probe keys emitted by older Aliyun CLI responses."""

    result = dict(container)
    mapping = {
        "LivenessProbeTcpSocketPort": ("LivenessProbe", "TcpSocket", "Port"),
        "LivenessProbeHttpGetPath": ("LivenessProbe", "HttpGet", "Path"),
        "LivenessProbeHttpGetPort": ("LivenessProbe", "HttpGet", "Port"),
        "LivenessProbeHttpGetScheme": ("LivenessProbe", "HttpGet", "Scheme"),
        "LivenessProbeFailureThreshold": ("LivenessProbe", "FailureThreshold"),
        "LivenessProbeInitialDelaySeconds": ("LivenessProbe", "InitialDelaySeconds"),
        "LivenessProbePeriodSeconds": ("LivenessProbe", "PeriodSeconds"),
        "LivenessProbeSuccessThreshold": ("LivenessProbe", "SuccessThreshold"),
        "LivenessProbeTimeoutSeconds": ("LivenessProbe", "TimeoutSeconds"),
        "ReadinessProbeTcpSocketPort": ("ReadinessProbe", "TcpSocket", "Port"),
        "ReadinessProbeHttpGetPath": ("ReadinessProbe", "HttpGet", "Path"),
        "ReadinessProbeHttpGetPort": ("ReadinessProbe", "HttpGet", "Port"),
        "ReadinessProbeHttpGetScheme": ("ReadinessProbe", "HttpGet", "Scheme"),
        "ReadinessProbeFailureThreshold": ("ReadinessProbe", "FailureThreshold"),
        "ReadinessProbeInitialDelaySeconds": ("ReadinessProbe", "InitialDelaySeconds"),
        "ReadinessProbePeriodSeconds": ("ReadinessProbe", "PeriodSeconds"),
        "ReadinessProbeSuccessThreshold": ("ReadinessProbe", "SuccessThreshold"),
        "ReadinessProbeTimeoutSeconds": ("ReadinessProbe", "TimeoutSeconds"),
    }
    for flat_key, path in mapping.items():
        if flat_key not in result:
            continue
        value = result.pop(flat_key)
        target = result.setdefault(path[0], {})
        if not isinstance(target, dict):
            fail(f"{path[0]}: conflicting probe representation")
        if len(path) == 2:
            if path[1] in target:
                fail(f"{path[0]}.{path[1]}: conflicting probe representation")
            target[path[1]] = value
            continue
        nested = target.setdefault(path[1], {})
        if not isinstance(nested, dict):
            fail(f"{path[0]}.{path[1]}: conflicting probe representation")
        if path[2] in nested:
            fail(f"{path[0]}.{path[1]}.{path[2]}: conflicting probe representation")
        nested[path[2]] = value
    return result


def select_config(document: Any, expected_id: str | None) -> dict[str, Any]:
    if not isinstance(document, dict):
        fail("response: expected object")
    configs = document.get("ScalingConfigurations")
    if not isinstance(configs, list) or not configs:
        fail("ScalingConfigurations: missing")
    selected = None
    if expected_id:
        for candidate in configs:
            if isinstance(candidate, dict) and candidate.get("ScalingConfigurationId") == expected_id:
                selected = candidate
                break
    if selected is None:
        if len(configs) == 1 and isinstance(configs[0], dict):
            selected = configs[0]
        else:
            fail("ScalingConfigurations: expected one matching configuration")
    return selected


def normalize_document(document: Any, expected_id: str | None = None) -> dict[str, Any]:
    config = select_config(document, expected_id)
    normalized: dict[str, Any] = {}

    # Normalize aliases before checking the root schema.
    for key, value in config.items():
        normalized[canonical_key(key)] = value
    allowed = (
        ROOT_SCALAR_FIELDS
        | ROOT_REPEAT_FIELDS
        | ROOT_OBJECT_LIST_FIELDS
        | {"Containers", "InitContainers", "DnsConfig"}
        | READ_ONLY_ROOT_FIELDS
    )
    reject_unknown(normalized, allowed, "ScalingConfigurations[0]")

    for key in list(normalized):
        value = normalized[key]
        if not is_populated(value):
            continue
        path = f"ScalingConfigurations[0].{key}"
        if key in READ_ONLY_ROOT_FIELDS:
            normalized.pop(key, None)
        elif key == "Containers":
            if not isinstance(value, list):
                fail(f"{path}: expected list")
            normalized[key] = [normalize_container(item, f"{path}[{i}]") for i, item in enumerate(value, 1)]
        elif key == "InitContainers":
            if not isinstance(value, list):
                fail(f"{path}: expected list")
            normalized[key] = [normalize_container(item, f"{path}[{i}]", init=True) for i, item in enumerate(value, 1)]
        elif key == "DnsConfig":
            normalized[key] = normalize_dns(value, path)
        elif key == "ImageRegistryCredential":
            normalized[key] = normalize_object_list(
                value,
                {"Server", "UserName", "Password"},
                path,
            )
        elif key == "AcrRegistryInfo":
            normalized[key] = normalize_object_list(value, ACR_FIELDS, path)
        elif key == "HostAliase":
            normalized[key] = normalize_object_list(value, HOST_ALIAS_FIELDS, path)
        elif key == "SecurityContextSysctl":
            normalized[key] = normalize_object_list(value, SYSCTL_FIELDS, path)
        elif key == "Tag":
            normalized[key] = normalize_object_list(value, TAG_FIELDS, path)
        elif key == "Volume":
            normalized[key] = normalize_volume(value, path)
        elif key in ROOT_REPEAT_FIELDS:
            if not isinstance(value, list):
                fail(f"{path}: expected list")
    return normalized


def set_probe(container: dict[str, Any], key: str, tcp: bool) -> None:
    old = container.get(key)
    probe = copy.deepcopy(old) if isinstance(old, dict) else {}
    for mechanism in ("Exec", "HttpGet", "TcpSocket"):
        probe.pop(mechanism, None)
    if tcp:
        probe["TcpSocket"] = {"Port": 3000}
        probe["InitialDelaySeconds"] = 20
    else:
        probe["HttpGet"] = {"Path": "/health/ready", "Port": 3000, "Scheme": "HTTP"}
        # /health/ready checks the configured dependencies. A zero delay makes
        # readiness reflect the application as soon as the process binds.
        probe["InitialDelaySeconds"] = 0
    probe["PeriodSeconds"] = 10
    probe["TimeoutSeconds"] = 5
    probe["FailureThreshold"] = 3
    container[key] = probe


def desired_document(document: Any, mode: str, digest: str | None, target_name: str | None) -> dict[str, Any]:
    normalized = normalize_document(document)
    result = copy.deepcopy(normalized)
    containers = result.get("Containers")
    if not isinstance(containers, list) or not containers:
        fail("ScalingConfigurations[0].Containers: missing")
    if mode == "target":
        if not digest or not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            fail("target image digest: invalid")
        target_index = 0
        if target_name:
            for index, container in enumerate(containers):
                if isinstance(container, dict) and container.get("Name") == target_name:
                    target_index = index
                    break
        target = containers[target_index]
        target["Image"] = f"docker.cnb.cool/imvhb/new-api-cn@{digest}"
        set_probe(target, "LivenessProbe", tcp=True)
        set_probe(target, "ReadinessProbe", tcp=False)
    elif mode != "restore":
        fail(f"mode: unsupported value {mode}")
    return result


def add_arg(args: list[str], path: str, value: Any) -> None:
    args.extend((path, validate_scalar(path, value)))


def add_generic(args: list[str], prefix: str, value: Any, path: str) -> None:
    if isinstance(value, dict):
        for key, item in value.items():
            add_generic(args, f"{prefix}.{key}", item, f"{path}.{key}")
    elif isinstance(value, list):
        for index, item in enumerate(value, 1):
            add_generic(args, f"{prefix}.{index}", item, f"{path}[{index}]")
    else:
        add_arg(args, prefix, value)


def serialize_probe(args: list[str], prefix: str, value: dict[str, Any], path: str) -> None:
    for key, item in value.items():
        if not is_populated(item):
            continue
        if key in PROBE_SCALAR_FIELDS:
            add_arg(args, f"{prefix}.{key}", item)
        elif key == "Exec":
            command = item.get("Command")
            if is_populated(command):
                for index, command_item in enumerate(command, 1):
                    add_arg(args, f"{prefix}.Exec.Command.{index}", command_item)
        elif key == "HttpGet":
            for subkey, subvalue in item.items():
                add_arg(args, f"{prefix}.HttpGet.{subkey}", subvalue)
        elif key == "TcpSocket":
            for subkey, subvalue in item.items():
                add_arg(args, f"{prefix}.TcpSocket.{subkey}", subvalue)
        else:
            fail(f"{path}.{key}: unsupported populated field")


def serialize_lifecycle(args: list[str], prefix: str, value: dict[str, Any], path: str) -> None:
    for key, item in value.items():
        if not is_populated(item):
            continue
        if key == "Exec":
            for index, command_item in enumerate(item.get("Command", []), 1):
                add_arg(args, f"{prefix}Exec.{index}", command_item)
        elif key == "HttpGet":
            for subkey, subvalue in item.items():
                add_arg(args, f"{prefix}HttpGet{subkey}", subvalue)
        elif key == "TcpSocket":
            for subkey, subvalue in item.items():
                add_arg(args, f"{prefix}TcpSocket{subkey}", subvalue)
        else:
            fail(f"{path}.{key}: unsupported populated field")


def serialize_container(args: list[str], index: int, container: dict[str, Any], path: str, init: bool = False) -> None:
    prefix = f"--{'InitContainer' if init else 'Container'}.{index}"
    for key, value in container.items():
        if not is_populated(value):
            continue
        item_path = f"{path}.{key}"
        if key in CONTAINER_SCALAR_FIELDS or (init and key in {"Cpu", "Gpu", "Image", "ImagePullPolicy", "Memory", "Name", "WorkingDir"}):
            add_arg(args, f"{prefix}.{key}", value)
        elif key in {"Arg", "Command"}:
            if not isinstance(value, list):
                fail(f"{item_path}: expected list")
            for item_index, item in enumerate(value, 1):
                add_arg(args, f"{prefix}.{key}.{item_index}", item)
        elif key == "Port":
            if not isinstance(value, list):
                value = [value]
            for item_index, item in enumerate(value, 1):
                add_generic(args, f"{prefix}.Port.{item_index}", item, f"{item_path}[{item_index}]")
        elif key == "EnvironmentVars":
            if not isinstance(value, list):
                fail(f"{item_path}: expected list")
            env_prefix = "InitContainerEnvironmentVar" if init else "EnvironmentVar"
            for item_index, item in enumerate(value, 1):
                for env_key, env_value in item.items():
                    # An empty FieldRefFieldPath means "no secret reference";
                    # sending it would overwrite the field with a blank value.
                    if env_key == "FieldRefFieldPath" and not env_value:
                        continue
                    add_arg(args, f"{prefix}.{env_prefix}.{item_index}.{env_key}", env_value)
        elif key == "VolumeMount":
            if not isinstance(value, list):
                fail(f"{item_path}: expected list")
            for item_index, item in enumerate(value, 1):
                add_generic(args, f"{prefix}.VolumeMount.{item_index}", item, f"{item_path}[{item_index}]")
        elif key in {"LivenessProbe", "ReadinessProbe"}:
            serialize_probe(args, f"{prefix}.{key}", value, item_path)
        elif key == "SecurityContext":
            add_generic(args, f"{prefix}.SecurityContext", value, item_path)
        elif key == "LifecyclePostStartHandler":
            serialize_lifecycle(args, f"{prefix}.LifecyclePostStartHandler", value, item_path)
        elif key == "LifecyclePreStopHandler":
            serialize_lifecycle(args, f"{prefix}.LifecyclePreStopHandler", value, item_path)
        else:
            fail(f"{item_path}: unsupported populated field")


def serialize_root(document: dict[str, Any]) -> list[str]:
    args: list[str] = []
    for key, value in document.items():
        if not is_populated(value):
            continue
        path = f"ScalingConfigurations[0].{key}"
        if key in ROOT_SCALAR_FIELDS:
            add_arg(args, f"--{key}", value)
        elif key in ROOT_REPEAT_FIELDS:
            if not isinstance(value, list):
                fail(f"{path}: expected list")
            for index, item in enumerate(value, 1):
                add_arg(args, f"--{key}.{index}", item)
        elif key == "DnsConfig":
            if "NameServer" in value:
                for index, item in enumerate(value["NameServer"], 1):
                    add_arg(args, f"--DnsConfigNameServer.{index}", item)
            if "Search" in value:
                for index, item in enumerate(value["Search"], 1):
                    add_arg(args, f"--DnsConfigSearch.{index}", item)
            for index, option in enumerate(value.get("Option", []), 1):
                for option_key, option_value in option.items():
                    add_arg(args, f"--DnsConfigOption.{index}.{option_key}", option_value)
        elif key == "ImageRegistryCredential":
            for index, credential in enumerate(value, 1):
                for credential_key, credential_value in credential.items():
                    add_arg(args, f"--ImageRegistryCredential.{index}.{credential_key}", credential_value)
        elif key == "AcrRegistryInfo":
            for index, registry in enumerate(value, 1):
                for registry_key, registry_value in registry.items():
                    add_generic(args, f"--AcrRegistryInfo.{index}.{registry_key}", registry_value, f"{path}[{index}].{registry_key}")
        elif key == "HostAliase":
            for index, alias in enumerate(value, 1):
                for alias_key, alias_value in alias.items():
                    add_generic(args, f"--HostAliase.{index}.{alias_key}", alias_value, f"{path}[{index}].{alias_key}")
        elif key == "SecurityContextSysctl":
            for index, sysctl in enumerate(value, 1):
                for field, item in sysctl.items():
                    add_arg(args, f"--SecurityContextSysctl.{index}.{field}", item)
        elif key == "Tag":
            for index, tag in enumerate(value, 1):
                for field, item in tag.items():
                    add_arg(args, f"--Tag.{index}.{field}", item)
        elif key == "Volume":
            for index, volume in enumerate(value, 1):
                add_generic(args, f"--Volume.{index}", volume, f"{path}[{index}]")
        elif key == "Containers":
            for index, container in enumerate(value, 1):
                serialize_container(args, index, container, f"{path}[{index}]")
        elif key == "InitContainers":
            for index, container in enumerate(value, 1):
                serialize_container(args, index, container, f"{path}[{index}]", init=True)
        else:
            fail(f"{path}: unsupported populated field")
    return args


def contains_subset(expected: Any, actual: Any, path: str = "ScalingConfigurations[0]") -> None:
    if isinstance(expected, dict):
        if not isinstance(actual, dict):
            fail(f"{path}: readback type drift")
        for key, value in expected.items():
            if key not in actual:
                fail(f"{path}.{key}: readback field missing")
            contains_subset(value, actual[key], f"{path}.{key}")
        return
    if isinstance(expected, list):
        if not isinstance(actual, list) or len(actual) < len(expected):
            fail(f"{path}: readback list truncated")
        for index, value in enumerate(expected):
            contains_subset(value, actual[index], f"{path}[{index + 1}]")
        return
    if expected != actual:
        fail(f"{path}: readback value drift")


def read_json_documents() -> list[Any]:
    raw = sys.stdin.read()
    decoder = json.JSONDecoder()
    documents: list[Any] = []
    offset = 0
    while offset < len(raw):
        while offset < len(raw) and raw[offset].isspace():
            offset += 1
        if offset >= len(raw):
            break
        document, end = decoder.raw_decode(raw, offset)
        documents.append(document)
        offset = end
    return documents


def emit_args(args: Iterable[str]) -> None:
    output = sys.stdout.buffer
    for value in args:
        output.write(value.encode("utf-8"))
        output.write(b"\0")


def main() -> int:
    parser = argparse.ArgumentParser(add_help=True)
    parser.add_argument("--mode", choices=("target", "restore"), required=True)
    parser.add_argument("--digest")
    parser.add_argument("--target-name", default="")
    parser.add_argument("--expected-id", default="")
    parser.add_argument("--verify", action="store_true")
    parser.add_argument("--emit", action="store_true")
    args = parser.parse_args()
    try:
        documents = read_json_documents()
        if args.verify:
            if len(documents) != 2:
                fail("verify: expected two JSON documents")
            expected = desired_document(documents[0], args.mode, args.digest, args.target_name or None)
            actual = normalize_document(documents[1], args.expected_id or None)
            contains_subset(expected, actual)
            return 0

        if len(documents) != 1:
            fail("input: expected one JSON document")
        expected = desired_document(documents[0], args.mode, args.digest, args.target_name or None)
        if args.emit:
            emit_args(serialize_root(expected))
        return 0
    except (ConfigError, json.JSONDecodeError) as error:
        print(f"config serialization failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
