// This file is originally from https://github.com/karalabe/usb
//
// usb - Self contained USB and HID library for Go
// Copyright 2019 The library Authors
//
// This library is free software: you can redistribute it and/or modify it under
// the terms of the GNU Lesser General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version.
//
// The library is distributed in the hope that it will be useful, but WITHOUT ANY
// WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR
// A PARTICULAR PURPOSE. See the GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License along
// with the library. If not, see <http://www.gnu.org/licenses/>.

//go:build none
// +build none

package main

import (
	"fmt"
	"strings"

	"github.com/karalabe/hid"
)

func main() {
	// Enumerate all the HID devices in alphabetical path order
	hids, err := hid.Enumerate(0, 0)
	if err != nil {
		panic(err)
	}
	for i := 0; i < len(hids); i++ {
		for j := i + 1; j < len(hids); j++ {
			if hids[i].Path > hids[j].Path {
				hids[i], hids[j] = hids[j], hids[i]
			}
		}
	}
	fmt.Printf("hid.Supported() %v\n", hid.Supported())
	for i, hid := range hids {
		fmt.Println(strings.Repeat("-", 128))
		fmt.Printf("HID #%d\n", i)
		fmt.Printf("  OS Path:      %s\n", hid.Path)
		fmt.Printf("  Vendor ID:    %#04x\n", hid.VendorID)
		fmt.Printf("  Product ID:   %#04x\n", hid.ProductID)
		fmt.Printf("  Release:      %d\n", hid.Release)
		fmt.Printf("  Serial:       %s\n", hid.Serial)
		fmt.Printf("  Manufacturer: %s\n", hid.Manufacturer)
		fmt.Printf("  Product:      %s\n", hid.Product)
		fmt.Printf("  Usage Page:   %#04x\n", hid.UsagePage)
		fmt.Printf("  Usage:        %d\n", hid.Usage)
		fmt.Printf("  Interface:    %d\n", hid.Interface)
	}
	fmt.Println(strings.Repeat("=", 128))
}
