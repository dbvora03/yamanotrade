// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {Script} from "forge-std/Script.sol";
import {MockUSDC} from "../src/MockUSDC.sol";
import {YamanotePredictionMarket} from "../src/YamanotePredictionMarket.sol";

/// @notice Deploys a funded local-only prototype against a standard Anvil node.
contract DeployAnvil is Script {
    uint256 private constant ORACLE_A = 0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80;
    uint256 private constant ORACLE_B = 0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d;
    uint256 private constant ORACLE_C = 0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a;
    bytes32 private constant START_TYPEHASH = keccak256("StartReport(uint256 seriesId,uint256 nonce,bytes32 trainIdHash,uint64 scheduledArrival)");

    function run() external returns (MockUSDC usdc, YamanotePredictionMarket market, uint256 seriesId) {
        address[] memory oracles = new address[](3);
        oracles[0] = vm.addr(ORACLE_A); oracles[1] = vm.addr(ORACLE_B); oracles[2] = vm.addr(ORACLE_C);
        vm.startBroadcast(ORACLE_A);
        usdc = new MockUSDC();
        market = new YamanotePredictionMarket(usdc, vm.addr(ORACLE_A), oracles, 2);
        seriesId = market.createSeries(YamanotePredictionMarket.SeriesConfig({
            targetStationHash: keccak256("odpt.Station:JR-East.Yamanote.Shibuya"),
            directionHash: keccak256("odpt.RailDirection:JR-East.Yamanote.Inner"),
            minDeviationSeconds: -150, bucketSizeSeconds: 5, bucketCount: 60,
            feeBps: 100, oracleTimeoutSeconds: 3600, feeRecipient: vm.addr(ORACLE_A)
        }));
        uint64 scheduled = uint64(block.timestamp + 600);
        bytes32 trainID = keccak256("fixture-clockwise-001");
        bytes32 structHash = keccak256(abi.encode(START_TYPEHASH, seriesId, 1, trainID, scheduled));
        bytes32 digest = keccak256(abi.encodePacked("\x19\x01", market.domainSeparator(), structHash));
        bytes[] memory signatures = new bytes[](2);
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(ORACLE_A, digest); signatures[0] = abi.encodePacked(r, s, v);
        (v, r, s) = vm.sign(ORACLE_B, digest); signatures[1] = abi.encodePacked(r, s, v);
        market.startSeries(YamanotePredictionMarket.StartReport(seriesId, 1, trainID, scheduled), signatures);
        usdc.mint(vm.addr(ORACLE_A), 10_000e6);
        vm.stopBroadcast();
    }
}
