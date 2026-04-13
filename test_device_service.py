#!/usr/bin/env python3
"""
Test script for Device Service gRPC endpoints
"""
import sys
import grpc
from google.protobuf import json_format, timestamp_pb2

# Compile proto files first
import subprocess
import os

proto_dir = os.path.join(os.path.dirname(__file__), 'api', 'proto')
out_dir = proto_dir

# Generate Python gRPC bindings
subprocess.run([
    'python', '-m', 'grpc_tools.protoc',
    '-I', proto_dir,
    '--python_out', out_dir,
    '--grpc_python_out', out_dir,
    os.path.join(proto_dir, 'device.proto')
], check=True)

sys.path.insert(0, proto_dir)
import device_pb2
import device_pb2_grpc


def test_create_device():
    """Test CreateDevice endpoint"""
    channel = grpc.insecure_channel('localhost:50051')
    stub = device_pb2_grpc.DeviceServiceStub(channel)
    
    # Create a test device
    request = device_pb2.CreateDeviceRequest(
        device_id='test-device-001',
        floor='floor-1',
        device_type='temperature-sensor',
        firmware_version='1.0.0'
    )
    
    print("Testing CreateDevice...")
    try:
        response = stub.CreateDevice(request, timeout=5)
        print(f"✓ CreateDevice successful")
        print(f"  Device ID: {response.device_id}")
        print(f"  Provisioned at: {response.provisioned_at.seconds}")
        return response.device_id
    except grpc.RpcError as e:
        print(f"✗ CreateDevice failed: {e.code()} - {e.details()}")
        return None


def test_get_device(device_id):
    """Test GetDevice endpoint"""
    channel = grpc.insecure_channel('localhost:50051')
    stub = device_pb2_grpc.DeviceServiceStub(channel)
    
    request = device_pb2.GetDeviceRequest(device_id=device_id)
    
    print("\nTesting GetDevice...")
    try:
        response = stub.GetDevice(request, timeout=5)
        print(f"✓ GetDevice successful")
        print(f"  Device: {response.device.device_id}")
        print(f"  Status: {response.device.status}")
        print(f"  Floor: {response.device.floor}")
    except grpc.RpcError as e:
        print(f"✗ GetDevice failed: {e.code()} - {e.details()}")


def test_list_devices():
    """Test ListDevices endpoint"""
    channel = grpc.insecure_channel('localhost:50051')
    stub = device_pb2_grpc.DeviceServiceStub(channel)
    
    request = device_pb2.ListDevicesRequest(limit=10, offset=0)
    
    print("\nTesting ListDevices...")
    try:
        response = stub.ListDevices(request, timeout=5)
        print(f"✓ ListDevices successful")
        print(f"  Total devices: {response.total_count}")
        print(f"  Devices returned: {len(response.devices)}")
        for device in response.devices[:3]:
            print(f"    - {device.device_id}")
    except grpc.RpcError as e:
        print(f"✗ ListDevices failed: {e.code()} - {e.details()}")


def test_update_device_status(device_id):
    """Test UpdateDeviceStatus endpoint"""
    channel = grpc.insecure_channel('localhost:50051')
    stub = device_pb2_grpc.DeviceServiceStub(channel)
    
    request = device_pb2.UpdateDeviceStatusRequest(
        device_id=device_id,
        new_status=device_pb2.DEVICE_STATUS_ACTIVE
    )
    
    print("\nTesting UpdateDeviceStatus...")
    try:
        response = stub.UpdateDeviceStatus(request, timeout=5)
        print(f"✓ UpdateDeviceStatus successful")
        print(f"  Device: {response.device.device_id}")
        print(f"  New status: {response.device.status}")
    except grpc.RpcError as e:
        print(f"✗ UpdateDeviceStatus failed: {e.code()} - {e.details()}")


if __name__ == '__main__':
    print("=" * 60)
    print("Device Service Integration Tests")
    print("=" * 60)
    
    try:
        device_id = test_create_device()
        if device_id:
            test_get_device(device_id)
            test_update_device_status(device_id)
        test_list_devices()
        
        print("\n" + "=" * 60)
        print("All tests completed!")
        print("=" * 60)
    except Exception as e:
        print(f"Error: {e}")
        sys.exit(1)
