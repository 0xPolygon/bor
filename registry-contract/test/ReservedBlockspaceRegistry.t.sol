// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Test} from "forge-std/Test.sol";
import {ReservedBlockspaceRegistry} from "../src/ReservedBlockspaceRegistry.sol";

contract ReservedBlockspaceRegistryTest is Test {
    ReservedBlockspaceRegistry internal registry;

    address internal owner = address(0xA11CE);
    address internal admin = address(0xB0B);
    address internal other = address(0xCAFE);
    address internal senderA = address(0x100);
    address internal senderB = address(0x200);
    address internal senderC = address(0x300);

    function setUp() public {
        registry = new ReservedBlockspaceRegistry();
        registry.initialize(owner, 80_000_000, 30_000_000);
    }

    function testInitializeCanOnlyRunOnce() public {
        vm.expectRevert(ReservedBlockspaceRegistry.AlreadyInitialized.selector);
        registry.initialize(owner, 80_000_000, 30_000_000);
    }

    function testOwnerCreatesClientAndBorFacingReadsWork() public {
        address[] memory addresses = new address[](2);
        addresses[0] = senderA;
        addresses[1] = senderB;

        vm.prank(owner);
        uint256 clientId = registry.createClient(admin, 20_000_000, "Polymarket", addresses);

        assertEq(clientId, 1);
        assertEq(registry.totalReservedGas(), 20_000_000);
        assertTrue(registry.isReservedAddress(senderA));
        assertTrue(registry.isReservedAddress(senderB));
        assertFalse(registry.isReservedAddress(senderC));

        (uint256 resolvedClientId, uint64 gasQuota, address resolvedAdmin, bool active) =
            registry.getClientForAddress(senderA);
        assertEq(resolvedClientId, clientId);
        assertEq(gasQuota, 20_000_000);
        assertEq(resolvedAdmin, admin);
        assertTrue(active);

        (address returnedAdmin, uint64 returnedQuota, bool returnedActive, string memory metadata, uint256 count) =
            registry.getClient(clientId);
        assertEq(returnedAdmin, admin);
        assertEq(returnedQuota, 20_000_000);
        assertTrue(returnedActive);
        assertEq(metadata, "Polymarket");
        assertEq(count, 2);
    }

    function testClientAdminCanManageAddresses() public {
        uint256 clientId = _createSingleAddressClient(senderA);

        vm.prank(admin);
        registry.addClientAddress(clientId, senderB);
        assertTrue(registry.isReservedAddress(senderB));

        vm.prank(admin);
        registry.removeClientAddress(clientId, senderA);
        assertFalse(registry.isReservedAddress(senderA));
        assertTrue(registry.isReservedAddress(senderB));

        address[] memory addresses = registry.getClientAddresses(clientId);
        assertEq(addresses.length, 1);
        assertEq(addresses[0], senderB);
    }

    function testNonAdminCannotManageAddresses() public {
        uint256 clientId = _createSingleAddressClient(senderA);

        vm.prank(other);
        vm.expectRevert(ReservedBlockspaceRegistry.NotClientAdmin.selector);
        registry.addClientAddress(clientId, senderB);
    }

    function testQuotaLimitsAreEnforced() public {
        _createSingleAddressClient(senderA);

        vm.prank(owner);
        vm.expectRevert(ReservedBlockspaceRegistry.MaxClientReservedGasExceeded.selector);
        registry.setLimits(60_000_000, 19_999_999);

        vm.prank(owner);
        registry.setLimits(60_000_000, 30_000_000);

        address[] memory addresses = new address[](1);
        addresses[0] = senderB;

        vm.prank(owner);
        vm.expectRevert(ReservedBlockspaceRegistry.MaxClientReservedGasExceeded.selector);
        registry.createClient(admin, 30_000_001, "too-large-client", addresses);

        vm.prank(owner);
        registry.createClient(admin, 30_000_000, "Courtyard", addresses);

        address[] memory moreAddresses = new address[](1);
        moreAddresses[0] = senderC;

        vm.prank(owner);
        vm.expectRevert(ReservedBlockspaceRegistry.MaxTotalReservedGasExceeded.selector);
        registry.createClient(admin, 20_000_000, "would-exceed-total", moreAddresses);
    }

    function testInactiveClientIsNotReservedButKeepsAddressMapping() public {
        uint256 clientId = _createSingleAddressClient(senderA);

        vm.prank(owner);
        registry.setClientActive(clientId, false);

        assertFalse(registry.isReservedAddress(senderA));
        assertEq(registry.getClientId(senderA), clientId);
        assertEq(registry.totalReservedGas(), 0);

        (uint256 resolvedClientId, uint64 gasQuota, address resolvedAdmin, bool active) =
            registry.getClientForAddress(senderA);
        assertEq(resolvedClientId, clientId);
        assertEq(gasQuota, 20_000_000);
        assertEq(resolvedAdmin, admin);
        assertFalse(active);
    }

    function testReservedClientListOnlyReturnsActiveClients() public {
        uint256 firstClientId = _createSingleAddressClient(senderA);

        address[] memory addresses = new address[](1);
        addresses[0] = senderB;

        vm.prank(owner);
        uint256 secondClientId = registry.createClient(admin, 10_000_000, "Courtyard", addresses);

        vm.prank(owner);
        registry.setClientActive(firstClientId, false);

        (uint256[] memory clientIds, address[] memory admins, uint64[] memory quotas) = registry.getReservedClients();
        assertEq(clientIds.length, 1);
        assertEq(admins.length, 1);
        assertEq(quotas.length, 1);
        assertEq(clientIds[0], secondClientId);
        assertEq(admins[0], admin);
        assertEq(quotas[0], 10_000_000);

        address[] memory whitelisted = registry.getWhitelistedAddresses();
        assertEq(whitelisted.length, 1);
        assertEq(whitelisted[0], senderB);
    }

    function _createSingleAddressClient(address account) internal returns (uint256 clientId) {
        address[] memory addresses = new address[](1);
        addresses[0] = account;

        vm.prank(owner);
        clientId = registry.createClient(admin, 20_000_000, "Polymarket", addresses);
    }
}
