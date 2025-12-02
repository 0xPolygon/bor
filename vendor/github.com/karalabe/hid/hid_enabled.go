// hid - Gopher Interface Devices (USB HID)
// Copyright (c) 2017 Péter Szilágyi. All rights reserved.
//
// This file is released under the 3-clause BSD license. Note however that Linux
// support depends on libusb, released under LGNU GPL 2.1 or later.

//go:build (freebsd && cgo) || (linux && cgo) || (darwin && !ios && cgo) || (windows && cgo)
// +build freebsd,cgo linux,cgo darwin,!ios,cgo windows,cgo

// Package hid provides an interface for USB HID devices.
package hid

/*
Linux/hidapi requires the 'libudev' package. Fedora:`dnf install systemd-devel`, Ubuntu `apt install libhidapi-dev`.
However, we prefer to not require libudev, which is why `libusb` is included, and enabled
specifically for the linux platform, below.
*/

/*
#cgo CFLAGS: -I./hidapi/hidapi
#cgo CFLAGS: -DDEFAULT_VISIBILITY=""
#cgo CFLAGS: -DPOLL_NFDS_TYPE=int

#cgo linux CFLAGS: -I./libusb/libusb -DOS_LINUX -D_GNU_SOURCE -DHAVE_SYS_TIME_H -DHAVE_CLOCK_GETTIME
#cgo linux,!android LDFLAGS: -lrt

#cgo darwin CFLAGS: -DOS_DARWIN -DHAVE_SYS_TIME_H
#cgo darwin LDFLAGS: -framework CoreFoundation -framework IOKit -lobjc

#cgo windows CFLAGS: -DOS_WINDOWS
#cgo windows LDFLAGS: -lsetupapi

#cgo freebsd CFLAGS: -DOS_FREEBSD
#cgo freebsd LDFLAGS: -lusb

#cgo openbsd CFLAGS: -DOS_OPENBSD

#if defined(OS_LINUX) || defined(OS_DARWIN) || defined(DOS_FREEBSD) || defined(OS_OPENBSD)
	#include <poll.h>
#endif

#ifdef OS_LINUX
	#include "os/events_posix.h"
	#include "os/threads_posix.h"

	#include "os/events_posix.c"
	#include "os/threads_posix.c"

	#include "os/linux_usbfs.c"
	#include "os/linux_netlink.c"

	#include "core.c"
	#include "descriptor.c"
	#include "hotplug.c"

	#include "io.c"
	#include "strerror.c"
	#include "sync.c"

   #include "hidapi/libusb/hid.c"

#elif OS_DARWIN
	#include "hidapi/mac/hid.c"
#elif OS_WINDOWS
	#include "hidapi/windows/hid.c"
#elif OS_FREEBSD
	#include <libusb.h>
	#include "hidapi/libusb/hid.c"
#elif DOS_OPENBSD
	#include "hidapi/libusb/hid.c"
#endif

*/
import "C"

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"
)

// enumerateLock is a mutex serializing access to USB device enumeration needed
// by the macOS USB HID system calls, which require 2 consecutive method calls
// for enumeration, causing crashes if called concurrently.
//
// For more details, see:
//
//	https://developer.apple.com/documentation/iokit/1438371-iohidmanagersetdevicematching
//	> "subsequent calls will cause the hid manager to release previously enumerated devices"
var enumerateLock sync.Mutex

// Supported returns whether this platform is supported by the HID library or not.
// The goal of this method is to allow programatically handling platforms that do
// not support HID and not having to fall back to build constraints.
func Supported() bool {
	return true
}

// Enumerate returns a list of all the HID devices attached to the system which
// match the vendor and product id:
//   - If the vendor id is set to 0 then any vendor matches.
//   - If the product id is set to 0 then any product matches.
//   - If the vendor and product id are both 0, all HID devices are returned.
func Enumerate(vendorID uint16, productID uint16) ([]DeviceInfo, error) {
	enumerateLock.Lock()
	defer enumerateLock.Unlock()

	// Gather all device infos and ensure they are freed before returning
	head := C.hid_enumerate(C.ushort(vendorID), C.ushort(productID))
	if head == nil {
		return nil, nil
	}
	defer C.hid_free_enumeration(head)

	// Iterate the list and retrieve the device details
	var infos []DeviceInfo
	for ; head != nil; head = head.next {
		info := DeviceInfo{
			Path:      C.GoString(head.path),
			VendorID:  uint16(head.vendor_id),
			ProductID: uint16(head.product_id),
			Release:   uint16(head.release_number),
			UsagePage: uint16(head.usage_page),
			Usage:     uint16(head.usage),
			Interface: int(head.interface_number),
		}
		if head.serial_number != nil {
			info.Serial, _ = wcharTToString(head.serial_number)
		}
		if head.product_string != nil {
			info.Product, _ = wcharTToString(head.product_string)
		}
		if head.manufacturer_string != nil {
			info.Manufacturer, _ = wcharTToString(head.manufacturer_string)
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// Open connects to a previsouly discovered HID device.
func (info DeviceInfo) Open() (Device, error) {
	enumerateLock.Lock()
	defer enumerateLock.Unlock()

	path := C.CString(info.Path)
	defer C.free(unsafe.Pointer(path))

	device := C.hid_open_path(path)
	if device == nil {
		return nil, errors.New("hidapi: failed to open device")
	}
	return &hidDevice{
		DeviceInfo: info,
		device:     device,
	}, nil
}

// hidDevice is a live HID USB connected device handle.
type hidDevice struct {
	DeviceInfo // Embed the infos for easier access

	device *C.hid_device // Low level HID device to communicate through
	lock   sync.Mutex
}

// Close releases the HID USB device handle.
func (dev *hidDevice) Close() error {
	dev.lock.Lock()
	defer dev.lock.Unlock()

	if dev.device != nil {
		C.hid_close(dev.device)
		dev.device = nil
	}
	return nil
}

// Write sends an output report to a HID device.
//
// Write will send the data on the first OUT endpoint, if one exists. If it does
// not, it will send the data through the Control Endpoint (Endpoint 0).
func (dev *hidDevice) Write(b []byte) (int, error) {
	// Abort if nothing to write
	if len(b) == 0 {
		return 0, nil
	}
	// Abort if device closed in between
	dev.lock.Lock()
	device := dev.device
	dev.lock.Unlock()

	if device == nil {
		return 0, ErrDeviceClosed
	}
	// Prepend a HID report ID on Windows, other OSes don't need it
	var report []byte
	fixlen := 0
	if runtime.GOOS == "windows" {
		report = append([]byte{0x00}, b...)
		fixlen = -1
	} else {
		report = b
	}
	// Execute the write operation
	written := int(C.hid_write(device, (*C.uchar)(&report[0]), C.size_t(len(report))))
	if written == -1 {
		// If the write failed, verify if closed or other error
		dev.lock.Lock()
		device = dev.device
		dev.lock.Unlock()

		if device == nil {
			return 0, ErrDeviceClosed
		}
		// Device not closed, some other error occurred
		message := C.hid_error(device)
		if message == nil {
			return 0, errors.New("hidapi: unknown failure")
		}
		failure, _ := wcharTToString(message)
		return 0, errors.New("hidapi: " + failure)
	}
	if written > 0 {
		written += fixlen
	}
	return written, nil
}

// Read retrieves an input report from a HID device, blocking and waiting for a response
func (dev *hidDevice) Read(b []byte) (int, error) {
	return dev.ReadTimeout(b, -1)
}

// ReadTimeout retrieves an input report from a HID device with a timeout. If timeout is -1, a
// blocking read is performed.
func (dev *hidDevice) ReadTimeout(b []byte, timeout int) (int, error) {
	// Abort if nothing to read
	if len(b) == 0 {
		return 0, nil
	}
	// Abort if device closed in between
	dev.lock.Lock()
	device := dev.device
	dev.lock.Unlock()

	if device == nil {
		return 0, ErrDeviceClosed
	}
	// Execute the read operation
	read := int(C.hid_read_timeout(device, (*C.uchar)(&b[0]), C.size_t(len(b)), C.int(timeout)))
	if read == -1 {
		// If the read failed, verify if closed or other error
		dev.lock.Lock()
		device = dev.device
		dev.lock.Unlock()

		if device == nil {
			return 0, ErrDeviceClosed
		}
		// Device not closed, some other error occurred
		message := C.hid_error(device)
		if message == nil {
			return 0, errors.New("hidapi: unknown failure")
		}
		failure, _ := wcharTToString(message)
		return 0, errors.New("hidapi: " + failure)
	}
	return read, nil
}

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
func (dev *hidDevice) SendFeatureReport(b []byte) (int, error) {
	// Abort if nothing to write
	if len(b) == 0 {
		return 0, nil
	}
	// Abort if device closed in between
	dev.lock.Lock()
	device := dev.device
	dev.lock.Unlock()

	if device == nil {
		return 0, ErrDeviceClosed
	}

	// Send the feature report
	written := int(C.hid_send_feature_report(device, (*C.uchar)(&b[0]), C.size_t(len(b))))
	if written == -1 {
		// If the write failed, verify if closed or other error
		dev.lock.Lock()
		device = dev.device
		dev.lock.Unlock()

		if device == nil {
			return 0, ErrDeviceClosed
		}
		// Device not closed, some other error occurred
		message := C.hid_error(device)
		if message == nil {
			return 0, errors.New("hidapi: unknown failure")
		}
		failure, _ := wcharTToString(message)
		return 0, errors.New("hidapi: " + failure)
	}
	return written, nil
}

// GetFeatureReport retreives a feature report from a HID device
//
// Set the first byte of []b to the Report ID of the report to be read. Make
// sure to allow space for this extra byte in []b. Upon return, the first byte
// will still contain the Report ID, and the report data will start in b[1].
func (dev *hidDevice) GetFeatureReport(b []byte) (int, error) {
	// Abort if we don't have anywhere to write the results
	if len(b) == 0 {
		return 0, nil
	}
	// Abort if device closed in between
	dev.lock.Lock()
	device := dev.device
	dev.lock.Unlock()

	if device == nil {
		return 0, ErrDeviceClosed
	}

	// Retrive the feature report
	read := int(C.hid_get_feature_report(device, (*C.uchar)(&b[0]), C.size_t(len(b))))
	if read == -1 {
		// If the read failed, verify if closed or other error
		dev.lock.Lock()
		device = dev.device
		dev.lock.Unlock()

		if device == nil {
			return 0, ErrDeviceClosed
		}

		// Device not closed, some other error occured
		message := C.hid_error(device)
		if message == nil {
			return 0, errors.New("hidapi: unknown failure")
		}
		failure, _ := wcharTToString(message)
		return 0, errors.New("hidapi: " + failure)
	}

	return read, nil
}
