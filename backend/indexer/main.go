package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	cursorID    = "eth_logs"
	blockBatch  = uint64(30)
	pollEvery   = 5 * time.Second
	requestTime = 10 * time.Second
)

const (
	seriesCreatedTopic          = "0xe14fe2a639a1212c80e90ec3baf664e696d33d63b66283504fdc78498f6a38d4"
	roundOpenedTopic            = "0x6ca316fda3a1f0253a45efefa2c905d371f5c940a48f53bafbf48697f4d1ce7a"
	rangeBetPlacedTopic         = "0x8be4fa8e7da49c4f33cf4e1d5378c8e1ec8b4961ce04332e1f43ad417c6d30d6"
	oracleReportAcceptedTopic   = "0x5417234394fdf203414451826bc7df720d3248cdb06be7db2d3bee62202ba38a"
	roundSettledTopic           = "0xbc2532c6c05e485b066c07d2f61c42d5b7eb2fbe1834a89a4e7b099c40604f1d"
	roundRefundedTopic          = "0xca68a3838e71b34ddc6d2d13a918259d3c8549adf11adafa6cbb87880ff883a8"
	ticketClaimedTopic          = "0x32f4bbf1cadf836f12eaa6b063a4012d2357ef9bfb64d52c0b752fd32612fe2a"
	ticketRefundedTopic         = "0x8c8fd3da31954f4657d981e2a89ea3421ef5d7bfa0998012eaff5f24a61a867a"
	oracleChangedTopic          = "0xc839b5877f623b583686377df854b924d71e9243578fb187b95913417b4861fe"
	oracleThresholdChangedTopic = "0x3d577b399b1a99340b21730c33903262f268223afadec6a1c2742fcdb5705b19"
	feesWithdrawnTopic          = "0xc0819c13be868895eb93e40eaceb96de976442fa1d404e5c55f14bb65a8c489a"
)

var errUnsupportedEvent = errors.New("unsupported event")

type config struct {
	rpcURL          string
	contractAddress string
	mongoURI        string
	database        string
	startBlock      *uint64
}

func loadConfig() (config, error) {
	c := config{
		rpcURL:          strings.TrimSpace(os.Getenv("ETH_RPC_URL")),
		contractAddress: strings.TrimSpace(os.Getenv("CONTRACT_ADDRESS")),
		mongoURI:        strings.TrimSpace(os.Getenv("MONGO_URI")),
		database:        strings.TrimSpace(os.Getenv("MONGO_DATABASE")),
	}
	if c.rpcURL == "" {
		return config{}, errors.New("ETH_RPC_URL is required")
	}
	if !validAddress(c.contractAddress) {
		return config{}, errors.New("CONTRACT_ADDRESS must be a 20-byte hex address")
	}
	if c.mongoURI == "" {
		c.mongoURI = "mongodb://localhost:27017"
	}
	if c.database == "" {
		c.database = "yamanotrade"
	}
	if raw := strings.TrimSpace(os.Getenv("START_BLOCK")); raw != "" {
		block, err := parseBlock(raw)
		if err != nil {
			return config{}, fmt.Errorf("invalid START_BLOCK: %w", err)
		}
		c.startBlock = &block
	}
	return c, nil
}

func validAddress(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}

func parseBlock(value string) (uint64, error) {
	base := 10
	if strings.HasPrefix(value, "0x") {
		base = 16
		value = strings.TrimPrefix(value, "0x")
	}
	return strconv.ParseUint(value, base, 64)
}

func hexBlock(block uint64) string { return fmt.Sprintf("0x%x", block) }

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type ethLog struct {
	Address          string   `json:"address" bson:"address"`
	Topics           []string `json:"topics" bson:"topics"`
	Data             string   `json:"data" bson:"data"`
	BlockNumber      string   `json:"blockNumber" bson:"block_number"`
	TransactionHash  string   `json:"transactionHash" bson:"transaction_hash"`
	TransactionIndex string   `json:"transactionIndex" bson:"transaction_index"`
	BlockHash        string   `json:"blockHash" bson:"block_hash"`
	LogIndex         string   `json:"logIndex" bson:"log_index"`
	Removed          bool     `json:"removed" bson:"removed"`
}

func (l ethLog) id() string { return l.TransactionHash + ":" + l.LogIndex }

// parseEvent decodes the ABI layout for each event emitted by
// YamanotePredictionMarket. uint256 values are stored as decimal strings to
// preserve their full Solidity range in MongoDB.
func parseEvent(entry ethLog) (string, bson.M, error) {
	if len(entry.Topics) == 0 {
		return "", nil, fmt.Errorf("%w: log has no topic", errUnsupportedEvent)
	}
	switch strings.ToLower(entry.Topics[0]) {
	case seriesCreatedTopic:
		indexed, _, err := eventWords(entry, 3, 0)
		if err != nil {
			return "", nil, err
		}
		doc := eventDocument(entry)
		doc["series_id"] = uintString(indexed[0])
		doc["station_hash"] = indexed[1]
		doc["direction_hash"] = indexed[2]
		return "series_created", doc, nil
	case roundOpenedTopic:
		indexed, data, err := eventWords(entry, 3, 2)
		if err != nil {
			return "", nil, err
		}
		doc := eventDocument(entry)
		doc["series_id"] = uintString(indexed[0])
		doc["round_id"] = uintString(indexed[1])
		doc["train_id_hash"] = indexed[2]
		doc["scheduled_arrival"] = uintString(data[0])
		doc["closes_at"] = uintString(data[1])
		return "round_opened", doc, nil
	case rangeBetPlacedTopic:
		indexed, data, err := eventWords(entry, 3, 5)
		if err != nil {
			return "", nil, err
		}
		firstBucket, err := uint16Value(data[1])
		if err != nil {
			return "", nil, err
		}
		lastBucket, err := uint16Value(data[2])
		if err != nil {
			return "", nil, err
		}
		doc := eventDocument(entry)
		doc["ticket_id"] = uintString(indexed[0])
		doc["series_id"] = uintString(indexed[1])
		doc["round_id"] = uintString(indexed[2])
		doc["bettor"] = addressValue(data[0])
		doc["first_bucket"] = firstBucket
		doc["last_bucket"] = lastBucket
		doc["amount"] = uintString(data[3])
		doc["claim_weight"] = uintString(data[4])
		return "range_bet_placed", doc, nil
	case oracleReportAcceptedTopic:
		indexed, data, err := eventWords(entry, 3, 1)
		if err != nil {
			return "", nil, err
		}
		doc := eventDocument(entry)
		doc["series_id"] = uintString(indexed[0])
		doc["round_id"] = uintString(indexed[1])
		doc["nonce"] = uintString(indexed[2])
		doc["arrival_window_end"] = uintString(data[0])
		return "oracle_report_accepted", doc, nil
	case roundSettledTopic:
		indexed, data, err := eventWords(entry, 2, 3)
		if err != nil {
			return "", nil, err
		}
		winningBucket, err := uint16Value(data[0])
		if err != nil {
			return "", nil, err
		}
		doc := eventDocument(entry)
		doc["series_id"] = uintString(indexed[0])
		doc["round_id"] = uintString(indexed[1])
		doc["winning_bucket"] = winningBucket
		doc["distributable"] = uintString(data[1])
		doc["fee"] = uintString(data[2])
		return "round_settled", doc, nil
	case roundRefundedTopic:
		indexed, data, err := eventWords(entry, 2, 1)
		if err != nil {
			return "", nil, err
		}
		doc := eventDocument(entry)
		doc["series_id"] = uintString(indexed[0])
		doc["round_id"] = uintString(indexed[1])
		doc["reason"] = data[0]
		return "round_refunded", doc, nil
	case ticketClaimedTopic:
		indexed, data, err := eventWords(entry, 2, 1)
		if err != nil {
			return "", nil, err
		}
		doc := eventDocument(entry)
		doc["ticket_id"] = uintString(indexed[0])
		doc["owner"] = addressValue(indexed[1])
		doc["amount"] = uintString(data[0])
		return "ticket_claimed", doc, nil
	case ticketRefundedTopic:
		indexed, data, err := eventWords(entry, 2, 1)
		if err != nil {
			return "", nil, err
		}
		doc := eventDocument(entry)
		doc["ticket_id"] = uintString(indexed[0])
		doc["owner"] = addressValue(indexed[1])
		doc["amount"] = uintString(data[0])
		return "ticket_refunded", doc, nil
	case oracleChangedTopic:
		indexed, data, err := eventWords(entry, 1, 1)
		if err != nil {
			return "", nil, err
		}
		enabled, err := boolValue(data[0])
		if err != nil {
			return "", nil, err
		}
		doc := eventDocument(entry)
		doc["oracle"] = addressValue(indexed[0])
		doc["enabled"] = enabled
		return "oracle_changed", doc, nil
	case oracleThresholdChangedTopic:
		_, data, err := eventWords(entry, 0, 1)
		if err != nil {
			return "", nil, err
		}
		doc := eventDocument(entry)
		doc["threshold"] = uintString(data[0])
		return "oracle_threshold_changed", doc, nil
	case feesWithdrawnTopic:
		indexed, data, err := eventWords(entry, 1, 1)
		if err != nil {
			return "", nil, err
		}
		doc := eventDocument(entry)
		doc["recipient"] = addressValue(indexed[0])
		doc["amount"] = uintString(data[0])
		return "fees_withdrawn", doc, nil
	default:
		return "", nil, fmt.Errorf("%w: %s", errUnsupportedEvent, entry.Topics[0])
	}
}

func eventDocument(entry ethLog) bson.M {
	return bson.M{
		"_id":               entry.id(),
		"address":           strings.ToLower(entry.Address),
		"block_number":      entry.BlockNumber,
		"block_hash":        entry.BlockHash,
		"transaction_hash":  entry.TransactionHash,
		"transaction_index": entry.TransactionIndex,
		"log_index":         entry.LogIndex,
		"removed":           entry.Removed,
	}
}

func eventWords(entry ethLog, indexedCount, dataCount int) ([]string, []string, error) {
	if len(entry.Topics) != indexedCount+1 {
		return nil, nil, fmt.Errorf("unexpected topic count for %s: got %d, want %d", entry.Topics[0], len(entry.Topics), indexedCount+1)
	}
	indexed := make([]string, indexedCount)
	for n := range indexed {
		word, err := abiWord(entry.Topics[n+1])
		if err != nil {
			return nil, nil, fmt.Errorf("indexed word %d: %w", n, err)
		}
		indexed[n] = word
	}
	rawData := strings.TrimPrefix(strings.ToLower(entry.Data), "0x")
	if len(rawData) != dataCount*64 {
		return nil, nil, fmt.Errorf("unexpected data size for %s: got %d words, want %d", entry.Topics[0], len(rawData)/64, dataCount)
	}
	data := make([]string, dataCount)
	for n := range data {
		word, err := abiWord("0x" + rawData[n*64:(n+1)*64])
		if err != nil {
			return nil, nil, fmt.Errorf("data word %d: %w", n, err)
		}
		data[n] = word
	}
	return indexed, data, nil
}

func abiWord(value string) (string, error) {
	raw := strings.TrimPrefix(strings.ToLower(value), "0x")
	if len(raw) != 64 {
		return "", errors.New("expected 32-byte ABI word")
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return "", errors.New("invalid ABI word")
	}
	return "0x" + raw, nil
}

func uintString(word string) string {
	value, _ := new(big.Int).SetString(strings.TrimPrefix(word, "0x"), 16)
	return value.String()
}

func uint16Value(word string) (int32, error) {
	value, _ := new(big.Int).SetString(strings.TrimPrefix(word, "0x"), 16)
	if !value.IsUint64() || value.Uint64() > math.MaxUint16 {
		return 0, errors.New("uint16 value out of range")
	}
	return int32(value.Uint64()), nil
}

func addressValue(word string) string { return "0x" + strings.TrimPrefix(word, "0x")[24:] }

func boolValue(word string) (bool, error) {
	switch uintString(word) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errors.New("invalid boolean value")
	}
}

type rpcClient struct {
	url             string
	contractAddress string
	client          *http.Client
}

func (c *rpcClient) call(ctx context.Context, method string, params any, result any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("RPC request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("RPC returned HTTP %d", resp.StatusCode)
	}
	var envelope rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode RPC response: %w", err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("RPC error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return errors.New("RPC response has no result")
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("decode RPC result: %w", err)
	}
	return nil
}

func (c *rpcClient) blockNumber(ctx context.Context) (uint64, error) {
	var value string
	if err := c.call(ctx, "eth_blockNumber", []any{}, &value); err != nil {
		return 0, err
	}
	return parseBlock(value)
}

func (c *rpcClient) logs(ctx context.Context, from, to uint64) ([]ethLog, error) {
	// Event topics can be added here later. An empty topics list matches every
	// event emitted by this contract in the requested block range.
	filter := map[string]any{
		"fromBlock": hexBlock(from),
		"toBlock":   hexBlock(to),
		"address":   c.contractAddress,
		"topics":    []any{},
	}
	var logs []ethLog
	if err := c.call(ctx, "eth_getLogs", []any{filter}, &logs); err != nil {
		return nil, err
	}
	return logs, nil
}

type cursor struct {
	ID        string `bson:"_id"`
	NextBlock int64  `bson:"next_block"`
}

type indexer struct {
	rpc      *rpcClient
	database *mongo.Database
	cursors  *mongo.Collection
}

func (i *indexer) initializeCursor(ctx context.Context, configured *uint64) error {
	var existing cursor
	err := i.cursors.FindOne(ctx, bson.M{"_id": cursorID}).Decode(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return err
	}

	var next uint64
	if configured != nil {
		next = *configured
	} else {
		head, err := i.rpc.blockNumber(ctx)
		if err != nil {
			return fmt.Errorf("read initial block: %w", err)
		}
		if head == math.MaxUint64 {
			return errors.New("initial block exceeds cursor range")
		}
		next = head + 1
	}
	if next > math.MaxInt64 {
		return errors.New("initial block exceeds MongoDB int64 range")
	}
	_, err = i.cursors.InsertOne(ctx, cursor{ID: cursorID, NextBlock: int64(next)})
	if mongo.IsDuplicateKeyError(err) {
		return nil
	}
	return err
}

func (i *indexer) poll(ctx context.Context) error {
	var saved cursor
	if err := i.cursors.FindOne(ctx, bson.M{"_id": cursorID}).Decode(&saved); err != nil {
		return fmt.Errorf("read cursor: %w", err)
	}
	if saved.NextBlock < 0 {
		return errors.New("stored cursor is negative")
	}
	start := uint64(saved.NextBlock)
	head, err := i.rpc.blockNumber(ctx)
	if err != nil {
		return fmt.Errorf("read latest block: %w", err)
	}
	if start > head {
		return nil
	}

	end := start + blockBatch - 1
	if end < start || end > head {
		end = head
	}
	if end >= uint64(math.MaxInt64) {
		return errors.New("cursor exceeds MongoDB int64 range")
	}
	logs, err := i.rpc.logs(ctx, start, end)
	if err != nil {
		return fmt.Errorf("fetch logs for blocks %d-%d: %w", start, end, err)
	}
	for _, entry := range logs {
		collection, document, err := parseEvent(entry)
		if errors.Is(err, errUnsupportedEvent) {
			log.Printf("skipping unsupported log %s", entry.id())
			continue
		}
		if err != nil {
			return fmt.Errorf("parse log %s: %w", entry.id(), err)
		}
		if _, err := i.database.Collection(collection).ReplaceOne(
			ctx,
			bson.M{"_id": entry.id()},
			document,
			options.Replace().SetUpsert(true),
		); err != nil {
			return fmt.Errorf("store %s event: %w", collection, err)
		}
	}
	result, err := i.cursors.UpdateOne(
		ctx,
		bson.M{"_id": cursorID, "next_block": saved.NextBlock},
		bson.M{"$set": bson.M{"next_block": int64(end + 1), "updated_at": time.Now().UTC()}},
	)
	if err != nil {
		return fmt.Errorf("advance cursor: %w", err)
	}
	if result.ModifiedCount != 1 {
		return errors.New("cursor changed during poll")
	}
	log.Printf("indexed blocks %d-%d (%d logs)", start, end, len(logs))
	return nil
}

func (i *indexer) run(ctx context.Context) {
	for {
		pollCtx, cancel := context.WithTimeout(ctx, requestTime)
		err := i.poll(pollCtx)
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("poll failed: %v", err)
		}

		timer := time.NewTimer(pollEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.mongoURI))
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		disconnectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mongoClient.Disconnect(disconnectCtx); err != nil {
			log.Printf("disconnect MongoDB: %v", err)
		}
	}()
	if err := mongoClient.Ping(ctx, nil); err != nil {
		log.Fatalf("connect to MongoDB: %v", err)
	}

	db := mongoClient.Database(cfg.database)
	service := &indexer{
		rpc: &rpcClient{
			url:             cfg.rpcURL,
			contractAddress: cfg.contractAddress,
			client:          &http.Client{Timeout: requestTime},
		},
		database: db,
		cursors:  db.Collection("indexer_cursors"),
	}
	if err := service.initializeCursor(ctx, cfg.startBlock); err != nil {
		log.Fatal(err)
	}
	service.run(ctx)
}
