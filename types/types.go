package types

type DexTx struct {
	ChainID    string `json:"chain_id"`
	Dex        string `json:"dex"`
	DexFactory string `json:"dex_factory"`
	Pool       string `json:"pool"`
	Block      uint64 `json:"block_height"`
	TxHash     string `json:"tx_hash"`
	TimeStamp  int64  `json:"time_stamp"`
	// Create, Buy, Sell
	Action       string `json:"action"`
	User         string `json:"user"`
	BaseAddress  string `json:"base_address,omitempty"`
	BaseSymbol   string `json:"base_symbol,omitempty"`
	BaseName     string `json:"base_name,omitempty"`
	BaseDecimal  string `json:"base_decimal,omitempty"`
	BaseAmount   string `json:"base_amount,omitempty"`
	QuoteAddress string `json:"quote_address"`
	QuoteSymbol  string `json:"quote_symbol,omitempty"`
	QuoteName    string `json:"quote_name,omitempty"`
	QuoteDecimal string `json:"quote_decimal,omitempty"`
	QuoteAmount  string `json:"quote_amount,omitempty"`
}
