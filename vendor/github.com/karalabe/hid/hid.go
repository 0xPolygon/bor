// hid - Gopher Interface Devices (USB HID)
// Copyright (c) 2017 Péter Szilágyi. All rights reserved.
//
// This file is released under the 3-clause BSD license. Note however that Linux
// support depends on libusb, released under GNU LGPL 2.1 or later.

// Package hid provides an interface for USB HID devices.
package hid

import "errors"

// ErrDeviceClosed is returned for operations where the device closed before or
// during the execution.
var ErrDeviceClosed = errors.New("hid: device closed")

// ErrUnsupportedPlatform is returned for all operations where the underlying
// operating system is not supported by the library.
var ErrUnsupportedPlatform = errors.New("hid: unsupported platform")

// DeviceInfo contains all the information we know about a USB device.
type DeviceInfo struct {
	Path         string // Platform-specific device path
	VendorID     uint16 // Device Vendor ID
	ProductID    uint16 // Device Product ID
	Release      uint16 // Device Release Number in binary-coded decimal, also known as Device Version Number
	Serial       string // Serial Number
	Manufacturer string // Manufacturer String
	Product      string // Product string
	UsagePage    uint16 // Usage Page for this Device/Interface (Windows/Mac only)
	Usage        uint16 // Usage for this Device/Interface (Windows/Mac only)

	// The USB interface which this logical device
	// represents. Valid on both Linux implementations
	// in all cases, and valid on the Windows implementation
	// only if the device contains more than one interface.
	Interface int
}

// Device is a generic USB device interface. It may either be backed by a USB HID
// device or a low level raw (libusb) device.
type Device interface {
	// Close releases the USB device handle.
	Close() error

	// Write sends a binary blob to a USB device. For HID devices write uses reports,
	// for low level USB write uses interrupt transfers.
	Write(b []byte) (int, error)

	// Read retrieves a binary blob from a USB device. For HID devices read uses
	// reports, for low level USB read uses interrupt transfers.
	Read(b []byte) (int, error)

	// Read retrieves a binary blob from a USB device, using a timeout. A timeout
	// of 0 means blocking.
	ReadTimeout(b []byte, timeout int) (int, error)

	// GetFeatureReport retreives a feature report from a HID device
	//
	// Set the first byte of []b to the Report ID of the report to be read. Make
	// sure to allow space for this extra byte in []b. Upon return, the first byte
	// will still contain the Report ID, and the report data will start in b[1].
	GetFeatureReport(b []byte) (int, error)

	// SendFeatureReport sends a feature report to a HID device
	//
	// Feature reports are sent over the Control endpoint as a Set_Report transfer.
	// The first byte of b must contain the Report ID. For devices which only
	// support a single report, this must be set to 0x0. The remaining bytes
	// contain the report data. Since the Report ID is mandatory, calls to
	// SendFeatureReport() will always contain one more byte than the report
	// contains. For example, if a hid report is 16 bytes long, 17 bytes must be
	// passed to SendFeatureReport(): the Report ID (or 0x0, for devices
	// which do not use numbered reports), followed by the report data (16 bytes).
	// In this example, the length passed in would be 17.
	SendFeatureReport(b []byte) (int, error)
}
