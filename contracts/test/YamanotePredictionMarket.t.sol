// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {Test} from "forge-std/Test.sol";
import {MockUSDC} from "../src/MockUSDC.sol";
import {YamanotePredictionMarket} from "../src/YamanotePredictionMarket.sol";

contract YamanotePredictionMarketTest is Test {
    bytes32 private constant START_TYPEHASH = keccak256("StartReport(uint256 seriesId,uint256 nonce,bytes32 trainIdHash,uint64 scheduledArrival)");
    bytes32 private constant UPDATE_TYPEHASH = keccak256("OracleReport(uint256 seriesId,uint256 roundId,uint256 nonce,bytes32 trainIdHash,bytes32 stationHash,uint64 scheduledArrival,uint64 arrivalWindowEnd,bytes32 nextTrainIdHash,uint64 nextScheduledArrival)");
    uint256 private constant ORACLE_A = 0xA11CE;
    uint256 private constant ORACLE_B = 0xB0B;
    uint256 private constant ORACLE_C = 0xCAFE;
    address private alice = makeAddr("alice");
    address private bob = makeAddr("bob");
    address private carol = makeAddr("carol");
    MockUSDC private usdc;
    YamanotePredictionMarket private market;
    uint256 private seriesId;
    bytes32 private station = keccak256("odpt.Station:JR-East.Yamanote.Shibuya");
    bytes32 private direction = keccak256("odpt.RailDirection:JR-East.Yamanote.Inner");
    bytes32 private train = keccak256("train-1");

    function setUp() public {
        vm.warp(1_000_000);
        usdc = new MockUSDC();
        address[] memory oracles = new address[](3);
        oracles[0] = vm.addr(ORACLE_A);
        oracles[1] = vm.addr(ORACLE_B);
        oracles[2] = vm.addr(ORACLE_C);
        market = new YamanotePredictionMarket(usdc, address(this), oracles, 2);
        seriesId = market.createSeries(YamanotePredictionMarket.SeriesConfig({
            targetStationHash: station,
            directionHash: direction,
            minDeviationSeconds: -150,
            bucketSizeSeconds: 5,
            bucketCount: 60,
            feeBps: 100,
            oracleTimeoutSeconds: 3600,
            feeRecipient: address(this)
        }));
        _start(train, uint64(block.timestamp + 400), 1);
        _fund(alice);
        _fund(bob);
        _fund(carol);
    }

    function testRangeSettlementAndAtomicRollover() public {
        vm.prank(alice);
        uint256 aliceTicket = market.placeRangeBet(seriesId, 1, 30, 30, 100e6);
        vm.warp(block.timestamp + 50);
        vm.prank(bob);
        uint256 bobTicket = market.placeRangeBet(seriesId, 1, 30, 31, 200e6);
        vm.prank(carol);
        market.placeRangeBet(seriesId, 1, 29, 29, 100e6);
        YamanotePredictionMarket.Round memory round = market.getRound(seriesId, 1);
        uint64 observed = uint64(uint256(round.scheduledArrival) + 2); // bucket 30: [0, 5)
        vm.warp(observed);
        _settle(round, observed, keccak256("train-2"), uint64(block.timestamp + 400), 2);
        YamanotePredictionMarket.Round memory settled = market.getRound(seriesId, 1);
        assertEq(uint256(settled.status), uint256(YamanotePredictionMarket.RoundStatus.Settled));
        assertEq(settled.winningBucket, 30);
        assertEq(market.getSeries(seriesId).activeRoundId, 2);
        uint256 beforeAlice = usdc.balanceOf(alice);
        vm.prank(alice);
        market.claim(aliceTicket);
        vm.prank(bob);
        market.claim(bobTicket);
        assertGt(usdc.balanceOf(alice), beforeAlice);
        assertEq(market.accruedFees(), 2e6);
    }

    function testOutOfRangeRefundsEveryTicket() public {
        vm.prank(alice);
        uint256 ticket = market.placeRangeBet(seriesId, 1, 0, 0, 100e6);
        YamanotePredictionMarket.Round memory round = market.getRound(seriesId, 1);
        uint64 observed = uint64(uint256(round.scheduledArrival) + 151);
        vm.warp(observed);
        _settle(round, observed, keccak256("train-2"), uint64(block.timestamp + 500), 3);
        assertEq(uint256(market.getRound(seriesId, 1).status), uint256(YamanotePredictionMarket.RoundStatus.Refunded));
        vm.prank(alice);
        market.claimRefund(ticket);
        assertEq(usdc.balanceOf(alice), 1_000e6);
    }

    function testRejectsBetAtClose() public {
        YamanotePredictionMarket.Round memory round = market.getRound(seriesId, 1);
        vm.warp(round.closesAt);
        vm.prank(alice);
        vm.expectRevert(YamanotePredictionMarket.BettingClosed.selector);
        market.placeRangeBet(seriesId, 1, 0, 0, 100e6);
    }

    function _fund(address user) private {
        usdc.mint(user, 1_000e6);
        vm.prank(user);
        usdc.approve(address(market), type(uint256).max);
    }

    function _start(bytes32 trainId, uint64 scheduled, uint256 nonce) private {
        bytes32 structHash = keccak256(abi.encode(START_TYPEHASH, seriesId, nonce, trainId, scheduled));
        bytes32 digest = keccak256(abi.encodePacked("\x19\x01", market.domainSeparator(), structHash));
        vm.prank(vm.addr(ORACLE_A));
        market.startSeries(YamanotePredictionMarket.StartReport(seriesId, nonce, trainId, scheduled), _twoSignatures(digest));
    }

    function _settle(YamanotePredictionMarket.Round memory round, uint64 observed, bytes32 nextTrain, uint64 nextSchedule, uint256 nonce) private {
        bytes32 structHash = keccak256(abi.encode(UPDATE_TYPEHASH, seriesId, 1, nonce, round.trainIdHash, station, round.scheduledArrival, observed, nextTrain, nextSchedule));
        bytes32 digest = keccak256(abi.encodePacked("\x19\x01", market.domainSeparator(), structHash));
        vm.prank(vm.addr(ORACLE_A));
        market.submitOracleUpdate(YamanotePredictionMarket.OracleReport(seriesId, 1, nonce, round.trainIdHash, station, round.scheduledArrival, observed, nextTrain, nextSchedule), _twoSignatures(digest));
    }

    function _twoSignatures(bytes32 digest) private returns (bytes[] memory signatures) {
        signatures = new bytes[](2);
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(ORACLE_A, digest);
        signatures[0] = abi.encodePacked(r, s, v);
        (v, r, s) = vm.sign(ORACLE_B, digest);
        signatures[1] = abi.encodePacked(r, s, v);
    }
}
