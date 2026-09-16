// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {EIP712} from "@openzeppelin/contracts/utils/cryptography/EIP712.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

/// @notice Pari-mutuel, non-transferable range tickets for recurring train-arrival markets.
/// @dev This contract is intentionally local-prototype scoped: a trusted, quorum-signed
///      oracle report is the source of truth and collateral is one immutable ERC-20.
contract YamanotePredictionMarket is AccessControl, Pausable, ReentrancyGuard, EIP712 {
    using SafeERC20 for IERC20;

    bytes32 public constant ORACLE_ROLE = keccak256("ORACLE_ROLE");
    uint16 public constant BPS = 10_000;
    uint16 public constant OPEN_WEIGHT_BPS = 10_000;
    uint16 public constant CLOSE_WEIGHT_BPS = 2_000;
    uint16 public constant MAX_BUCKETS = 60;
    bytes32 private constant START_REPORT_TYPEHASH = keccak256(
        "StartReport(uint256 seriesId,uint256 nonce,bytes32 trainIdHash,uint64 scheduledArrival)"
    );
    bytes32 private constant ORACLE_REPORT_TYPEHASH = keccak256(
        "OracleReport(uint256 seriesId,uint256 roundId,uint256 nonce,bytes32 trainIdHash,bytes32 stationHash,uint64 scheduledArrival,uint64 arrivalWindowEnd,bytes32 nextTrainIdHash,uint64 nextScheduledArrival)"
    );

    enum RoundStatus { None, Open, Settled, Refunded }

    struct SeriesConfig {
        bytes32 targetStationHash;
        bytes32 directionHash;
        int32 minDeviationSeconds;
        uint32 bucketSizeSeconds;
        uint16 bucketCount;
        uint16 feeBps;
        uint64 oracleTimeoutSeconds;
        address feeRecipient;
    }

    struct Series {
        SeriesConfig config;
        uint256 activeRoundId;
        uint256 nextRoundId;
        bool exists;
    }

    struct Round {
        bytes32 trainIdHash;
        uint64 scheduledArrival;
        uint64 openedAt;
        uint64 closesAt;
        uint64 arrivalWindowEnd;
        uint256 totalPool;
        uint256 winningRawAllocation;
        uint256 winningWeight;
        uint256 distributable;
        uint16 winningBucket;
        RoundStatus status;
    }

    struct Ticket {
        address owner;
        uint256 seriesId;
        uint256 roundId;
        uint256 amount;
        uint256 perBucketAllocation;
        uint256 claimWeight;
        uint16 firstBucket;
        uint16 lastBucket;
        bool claimed;
    }

    struct StartReport {
        uint256 seriesId;
        uint256 nonce;
        bytes32 trainIdHash;
        uint64 scheduledArrival;
    }

    struct OracleReport {
        uint256 seriesId;
        uint256 roundId;
        uint256 nonce;
        bytes32 trainIdHash;
        bytes32 stationHash;
        uint64 scheduledArrival;
        uint64 arrivalWindowEnd;
        bytes32 nextTrainIdHash;
        uint64 nextScheduledArrival;
    }

    IERC20 public immutable collateral;
    uint256 public seriesCount;
    uint256 public ticketCount;
    uint256 public accruedFees;
    uint256 public oracleCount;
    uint256 public oracleThreshold;

    mapping(uint256 => Series) private _series;
    mapping(uint256 => mapping(uint256 => Round)) private _rounds;
    mapping(uint256 => Ticket) private _tickets;
    mapping(uint256 => bool) public usedOracleNonce;
    mapping(address => uint256) public accruedFeesByRecipient;
    // A range ticket updates start and end delta accumulators. Prefixing at settlement
    // yields the allocation and decayed weight in a selected bucket without O(width) writes.
    mapping(uint256 => mapping(uint256 => mapping(uint256 => uint256))) private _rawStarts;
    mapping(uint256 => mapping(uint256 => mapping(uint256 => uint256))) private _rawEnds;
    mapping(uint256 => mapping(uint256 => mapping(uint256 => uint256))) private _weightStarts;
    mapping(uint256 => mapping(uint256 => mapping(uint256 => uint256))) private _weightEnds;

    error InvalidSeriesConfig();
    error UnknownSeries();
    error NoActiveRound();
    error RoundNotOpen();
    error BettingClosed();
    error InvalidRange();
    error InvalidAmount();
    error InvalidSchedule();
    error InvalidReport();
    error FutureObservation();
    error NonceAlreadyUsed();
    error InsufficientOracleSignatures();
    error DuplicateOracleSignature();
    error TicketNotClaimable();
    error TicketAlreadyClaimed();
    error NotTicketOwner();
    error NotWinningTicket();
    error TimeoutNotReached();

    event SeriesCreated(uint256 indexed seriesId, bytes32 indexed stationHash, bytes32 indexed directionHash);
    event RoundOpened(uint256 indexed seriesId, uint256 indexed roundId, bytes32 indexed trainIdHash, uint64 scheduledArrival, uint64 closesAt);
    event RangeBetPlaced(uint256 indexed ticketId, uint256 indexed seriesId, uint256 indexed roundId, address bettor, uint16 firstBucket, uint16 lastBucket, uint256 amount, uint256 claimWeight);
    event OracleReportAccepted(uint256 indexed seriesId, uint256 indexed roundId, uint256 indexed nonce, uint64 arrivalWindowEnd);
    event RoundSettled(uint256 indexed seriesId, uint256 indexed roundId, uint16 winningBucket, uint256 distributable, uint256 fee);
    event RoundRefunded(uint256 indexed seriesId, uint256 indexed roundId, bytes32 reason);
    event TicketClaimed(uint256 indexed ticketId, address indexed owner, uint256 amount);
    event TicketRefunded(uint256 indexed ticketId, address indexed owner, uint256 amount);
    event OracleChanged(address indexed oracle, bool enabled);
    event OracleThresholdChanged(uint256 threshold);
    event FeesWithdrawn(address indexed recipient, uint256 amount);

    constructor(IERC20 collateral_, address admin, address[] memory initialOracles, uint256 threshold)
        EIP712("YamanotePredictionMarket", "1")
    {
        if (address(collateral_) == address(0) || admin == address(0)) revert InvalidSeriesConfig();
        collateral = collateral_;
        _grantRole(DEFAULT_ADMIN_ROLE, admin);
        for (uint256 i; i < initialOracles.length; ++i) {
            if (!hasRole(ORACLE_ROLE, initialOracles[i])) {
                _grantRole(ORACLE_ROLE, initialOracles[i]);
                ++oracleCount;
            }
        }
        if (threshold == 0 || threshold > oracleCount) revert InvalidSeriesConfig();
        oracleThreshold = threshold;
    }

    function createSeries(SeriesConfig calldata config) external onlyRole(DEFAULT_ADMIN_ROLE) returns (uint256 seriesId) {
        if (
            config.targetStationHash == bytes32(0) || config.directionHash == bytes32(0) || config.bucketSizeSeconds == 0 ||
            config.bucketCount == 0 || config.bucketCount > MAX_BUCKETS || config.feeBps > BPS || config.feeRecipient == address(0)
        ) revert InvalidSeriesConfig();
        seriesId = ++seriesCount;
        _series[seriesId] = Series({config: config, activeRoundId: 0, nextRoundId: 1, exists: true});
        emit SeriesCreated(seriesId, config.targetStationHash, config.directionHash);
    }

    function setOracle(address oracle, bool enabled) external onlyRole(DEFAULT_ADMIN_ROLE) {
        if (oracle == address(0)) revert InvalidSeriesConfig();
        bool exists = hasRole(ORACLE_ROLE, oracle);
        if (enabled && !exists) {
            _grantRole(ORACLE_ROLE, oracle);
            ++oracleCount;
        } else if (!enabled && exists) {
            if (oracleCount <= oracleThreshold) revert InvalidSeriesConfig();
            _revokeRole(ORACLE_ROLE, oracle);
            --oracleCount;
        }
        emit OracleChanged(oracle, enabled);
    }

    function setOracleThreshold(uint256 threshold) external onlyRole(DEFAULT_ADMIN_ROLE) {
        if (threshold == 0 || threshold > oracleCount) revert InvalidSeriesConfig();
        oracleThreshold = threshold;
        emit OracleThresholdChanged(threshold);
    }

    function pause() external onlyRole(DEFAULT_ADMIN_ROLE) { _pause(); }
    function unpause() external onlyRole(DEFAULT_ADMIN_ROLE) { _unpause(); }

    function startSeries(StartReport calldata report, bytes[] calldata signatures) external onlyRole(ORACLE_ROLE) whenNotPaused {
        Series storage series = _requireSeries(report.seriesId);
        if (series.activeRoundId != 0 || report.trainIdHash == bytes32(0)) revert InvalidReport();
        _useNonceAndVerify(report.nonce, _hashTypedDataV4(keccak256(abi.encode(START_REPORT_TYPEHASH, report.seriesId, report.nonce, report.trainIdHash, report.scheduledArrival))), signatures);
        _openRound(series, report.seriesId, report.trainIdHash, report.scheduledArrival);
    }

    function placeRangeBet(uint256 seriesId, uint256 roundId, uint16 firstBucket, uint16 lastBucket, uint256 amount)
        external
        whenNotPaused
        nonReentrant
        returns (uint256 ticketId)
    {
        Series storage series = _requireSeries(seriesId);
        if (series.activeRoundId != roundId) revert RoundNotOpen();
        Round storage round = _rounds[seriesId][roundId];
        if (round.status != RoundStatus.Open) revert RoundNotOpen();
        if (block.timestamp >= round.closesAt) revert BettingClosed();
        if (firstBucket > lastBucket || lastBucket >= series.config.bucketCount) revert InvalidRange();
        uint256 width = uint256(lastBucket) - firstBucket + 1;
        if (amount == 0 || amount % width != 0) revert InvalidAmount();
        uint256 allocation = amount / width;
        uint256 weight = allocation * currentMultiplierBps(seriesId, roundId);
        if (weight == 0) revert BettingClosed();

        collateral.safeTransferFrom(msg.sender, address(this), amount);
        round.totalPool += amount;
        _rawStarts[seriesId][roundId][firstBucket] += allocation;
        _weightStarts[seriesId][roundId][firstBucket] += weight;
        uint256 endExclusive = uint256(lastBucket) + 1;
        _rawEnds[seriesId][roundId][endExclusive] += allocation;
        _weightEnds[seriesId][roundId][endExclusive] += weight;
        ticketId = ++ticketCount;
        _tickets[ticketId] = Ticket(msg.sender, seriesId, roundId, amount, allocation, weight, firstBucket, lastBucket, false);
        emit RangeBetPlaced(ticketId, seriesId, roundId, msg.sender, firstBucket, lastBucket, amount, weight);
    }

    function submitOracleUpdate(OracleReport calldata report, bytes[] calldata signatures) external onlyRole(ORACLE_ROLE) whenNotPaused {
        Series storage series = _requireSeries(report.seriesId);
        if (series.activeRoundId != report.roundId) revert NoActiveRound();
        Round storage round = _rounds[report.seriesId][report.roundId];
        if (round.status != RoundStatus.Open || report.stationHash != series.config.targetStationHash || report.trainIdHash != round.trainIdHash || report.scheduledArrival != round.scheduledArrival || report.nextTrainIdHash == bytes32(0)) revert InvalidReport();
        if (report.arrivalWindowEnd > block.timestamp) revert FutureObservation();
        if (report.nextScheduledArrival <= block.timestamp) revert InvalidSchedule();
        _useNonceAndVerify(report.nonce, _oracleReportDigest(report), signatures);
        emit OracleReportAccepted(report.seriesId, report.roundId, report.nonce, report.arrivalWindowEnd);
        round.arrivalWindowEnd = report.arrivalWindowEnd;
        series.activeRoundId = 0;

        // A target confirmation before close would make its result public during betting.
        if (report.arrivalWindowEnd <= round.closesAt) {
            round.status = RoundStatus.Refunded;
            emit RoundRefunded(report.seriesId, report.roundId, keccak256("ARRIVED_BEFORE_CLOSE"));
        } else {
            _resolveRound(series, report.seriesId, report.roundId, report.arrivalWindowEnd);
        }
        _openRound(series, report.seriesId, report.nextTrainIdHash, report.nextScheduledArrival);
    }

    function refundTimedOutRound(uint256 seriesId) external onlyRole(DEFAULT_ADMIN_ROLE) {
        Series storage series = _requireSeries(seriesId);
        uint256 roundId = series.activeRoundId;
        if (roundId == 0) revert NoActiveRound();
        Round storage round = _rounds[seriesId][roundId];
        if (block.timestamp <= uint256(round.scheduledArrival) + series.config.oracleTimeoutSeconds) revert TimeoutNotReached();
        round.status = RoundStatus.Refunded;
        series.activeRoundId = 0;
        emit RoundRefunded(seriesId, roundId, keccak256("ORACLE_TIMEOUT"));
    }

    function claim(uint256 ticketId) external nonReentrant returns (uint256 payout) {
        Ticket storage ticket = _tickets[ticketId];
        if (ticket.owner != msg.sender) revert NotTicketOwner();
        if (ticket.claimed) revert TicketAlreadyClaimed();
        Round storage round = _rounds[ticket.seriesId][ticket.roundId];
        if (round.status != RoundStatus.Settled) revert TicketNotClaimable();
        if (round.winningBucket < ticket.firstBucket || round.winningBucket > ticket.lastBucket) revert NotWinningTicket();
        ticket.claimed = true;
        payout = ticket.claimWeight * round.distributable / round.winningWeight;
        collateral.safeTransfer(ticket.owner, payout);
        emit TicketClaimed(ticketId, ticket.owner, payout);
    }

    function claimRefund(uint256 ticketId) external nonReentrant returns (uint256 amount) {
        Ticket storage ticket = _tickets[ticketId];
        if (ticket.owner != msg.sender) revert NotTicketOwner();
        if (ticket.claimed) revert TicketAlreadyClaimed();
        if (_rounds[ticket.seriesId][ticket.roundId].status != RoundStatus.Refunded) revert TicketNotClaimable();
        ticket.claimed = true;
        amount = ticket.amount;
        collateral.safeTransfer(ticket.owner, amount);
        emit TicketRefunded(ticketId, ticket.owner, amount);
    }

    function withdrawFees(address recipient) external onlyRole(DEFAULT_ADMIN_ROLE) nonReentrant returns (uint256 amount) {
        amount = accruedFeesByRecipient[recipient];
        accruedFeesByRecipient[recipient] = 0;
        accruedFees -= amount;
        collateral.safeTransfer(recipient, amount);
        emit FeesWithdrawn(recipient, amount);
    }

    function currentMultiplierBps(uint256 seriesId, uint256 roundId) public view returns (uint256) {
        Round storage round = _rounds[seriesId][roundId];
        if (round.status != RoundStatus.Open || block.timestamp >= round.closesAt) return CLOSE_WEIGHT_BPS;
        uint256 duration = uint256(round.closesAt) - round.openedAt;
        if (duration == 0) return CLOSE_WEIGHT_BPS;
        uint256 elapsed = block.timestamp - round.openedAt;
        return OPEN_WEIGHT_BPS - ((OPEN_WEIGHT_BPS - CLOSE_WEIGHT_BPS) * elapsed / duration);
    }

    /// @notice Exposes the EIP-712 domain separator for local oracle signers.
    function domainSeparator() external view returns (bytes32) { return _domainSeparatorV4(); }

    function getSeries(uint256 seriesId) external view returns (Series memory) { return _series[seriesId]; }
    function getRound(uint256 seriesId, uint256 roundId) external view returns (Round memory) { return _rounds[seriesId][roundId]; }
    function getTicket(uint256 ticketId) external view returns (Ticket memory) { return _tickets[ticketId]; }

    function bucketTotals(uint256 seriesId, uint256 roundId, uint16 bucket) public view returns (uint256 rawAllocation, uint256 claimWeight) {
        Series storage series = _series[seriesId];
        if (!series.exists || bucket >= series.config.bucketCount) revert InvalidRange();
        for (uint256 i; i <= bucket; ++i) {
            rawAllocation += _rawStarts[seriesId][roundId][i];
            rawAllocation -= _rawEnds[seriesId][roundId][i];
            claimWeight += _weightStarts[seriesId][roundId][i];
            claimWeight -= _weightEnds[seriesId][roundId][i];
        }
    }

    function previewTicketPayout(uint256 ticketId) external view returns (uint256) {
        Ticket storage ticket = _tickets[ticketId];
        Round storage round = _rounds[ticket.seriesId][ticket.roundId];
        if (round.status != RoundStatus.Settled || round.winningBucket < ticket.firstBucket || round.winningBucket > ticket.lastBucket) return 0;
        return ticket.claimWeight * round.distributable / round.winningWeight;
    }

    function _resolveRound(Series storage series, uint256 seriesId, uint256 roundId, uint64 arrivalWindowEnd) private {
        Round storage round = _rounds[seriesId][roundId];
        int256 deviation = int256(uint256(arrivalWindowEnd)) - int256(uint256(round.scheduledArrival));
        int256 min = int256(series.config.minDeviationSeconds);
        int256 upper = min + int256(uint256(series.config.bucketSizeSeconds) * series.config.bucketCount);
        if (deviation < min || deviation >= upper) {
            round.status = RoundStatus.Refunded;
            emit RoundRefunded(seriesId, roundId, keccak256("OUT_OF_RANGE"));
            return;
        }
        uint16 bucket = uint16(uint256((deviation - min) / int256(uint256(series.config.bucketSizeSeconds))));
        (uint256 rawAllocation, uint256 weight) = bucketTotals(seriesId, roundId, bucket);
        if (rawAllocation == 0 || weight == 0) {
            round.status = RoundStatus.Refunded;
            emit RoundRefunded(seriesId, roundId, keccak256("EMPTY_WINNING_BUCKET"));
            return;
        }
        uint256 losingPool = round.totalPool - rawAllocation;
        uint256 fee = losingPool * series.config.feeBps / BPS;
        round.winningBucket = bucket;
        round.winningRawAllocation = rawAllocation;
        round.winningWeight = weight;
        round.distributable = round.totalPool - fee;
        round.status = RoundStatus.Settled;
        accruedFees += fee;
        accruedFeesByRecipient[series.config.feeRecipient] += fee;
        emit RoundSettled(seriesId, roundId, bucket, round.distributable, fee);
    }

    function _openRound(Series storage series, uint256 seriesId, bytes32 trainIdHash, uint64 scheduledArrival) private {
        if (scheduledArrival <= block.timestamp || trainIdHash == bytes32(0)) revert InvalidSchedule();
        uint64 openedAt = uint64(block.timestamp);
        uint64 closesAt = openedAt + (scheduledArrival - openedAt) / 4;
        if (closesAt <= openedAt) revert InvalidSchedule();
        uint256 roundId = series.nextRoundId++;
        series.activeRoundId = roundId;
        _rounds[seriesId][roundId] = Round(trainIdHash, scheduledArrival, openedAt, closesAt, 0, 0, 0, 0, 0, 0, RoundStatus.Open);
        emit RoundOpened(seriesId, roundId, trainIdHash, scheduledArrival, closesAt);
    }

    function _requireSeries(uint256 seriesId) private view returns (Series storage series) {
        series = _series[seriesId];
        if (!series.exists) revert UnknownSeries();
    }

    function _useNonceAndVerify(uint256 nonce, bytes32 digest, bytes[] calldata signatures) private {
        if (usedOracleNonce[nonce]) revert NonceAlreadyUsed();
        _verifySignatures(digest, signatures);
        usedOracleNonce[nonce] = true;
    }

    function _oracleReportDigest(OracleReport calldata report) private view returns (bytes32) {
        return _hashTypedDataV4(keccak256(abi.encode(
            ORACLE_REPORT_TYPEHASH, report.seriesId, report.roundId, report.nonce, report.trainIdHash, report.stationHash,
            report.scheduledArrival, report.arrivalWindowEnd, report.nextTrainIdHash, report.nextScheduledArrival
        )));
    }

    function _verifySignatures(bytes32 digest, bytes[] calldata signatures) private view {
        if (signatures.length < oracleThreshold) revert InsufficientOracleSignatures();
        address[] memory seen = new address[](signatures.length);
        uint256 valid;
        for (uint256 i; i < signatures.length; ++i) {
            address signer = ECDSA.recover(digest, signatures[i]);
            if (!hasRole(ORACLE_ROLE, signer)) revert InsufficientOracleSignatures();
            for (uint256 j; j < i; ++j) if (seen[j] == signer) revert DuplicateOracleSignature();
            seen[i] = signer;
            ++valid;
        }
        if (valid < oracleThreshold) revert InsufficientOracleSignatures();
    }
}
