package mobile

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/skycoin/skycoin-exchange/src/coin"
	"github.com/skycoin/skycoin-exchange/src/sknet"
	"github.com/skycoin/skycoin-exchange/src/wallet"
	bip39 "github.com/tyler-smith/go-bip39"
)

// gobind doc: https://godoc.org/golang.org/x/mobile/cmd/gobind
var config Config
var coinMap map[string]Coiner

// Config used for init the api env, includes wallet dir path, skycoin node and bitcoin node address.
// the node address is consisted of ip and port, eg: 127.0.0.1:6420
type Config struct {
	WalletDirPath string `json:"wallet_dir_path"`
	ServerAddr    string `json:"server_addr"`
	ServerPubkey  string `json:"server_pubkey"`
}

// NewConfig create config instance.
func NewConfig() *Config {
	return &Config{}
}

// Init initialize wallet dir and node instance.
func Init(cfg *Config) {
	initConfig(cfg,
		newCoin("skycoin", config.ServerAddr),
		newCoin("mzcoin", config.ServerAddr),
		newBitcoin(config.ServerAddr))
}

func initConfig(cfg *Config, coins ...Coiner) {
	if cfg.ServerPubkey != "" {
		sknet.SetPubkey(cfg.ServerPubkey)
	}

	wallet.InitDir(cfg.WalletDirPath)
	config = *cfg

	coinMap = make(map[string]Coiner)
	for i := range coins {
		coinMap[coins[i].Name()] = coins[i]
	}
}

// NewWallet create a new wallet base on the wallet type and seed
func NewWallet(coinType string, seed string) (string, error) {
	wlt, err := wallet.New(coinType, seed)
	if err != nil {
		return "", err
	}
	return wlt.GetID(), nil
}

// NewAddress generate address in specific wallet.
func NewAddress(walletID string, num int) (string, error) {
	es, err := wallet.NewAddresses(walletID, num)
	if err != nil {
		return "", err
	}
	var res = struct {
		Entries []coin.AddressEntry `json:"addresses"`
	}{
		es,
	}
	d, err := json.Marshal(res)
	if err != nil {
		return "", err
	}

	return string(d), nil
}

// GetAddresses return all addresses in the wallet.
func GetAddresses(walletID string) (string, error) {
	addrs, err := wallet.GetAddresses(walletID)
	if err != nil {
		return "", err
	}
	var res = struct {
		Addresses []string `json:"addresses"`
	}{
		addrs,
	}

	d, err := json.Marshal(res)
	if err != nil {
		return "", err
	}

	return string(d), nil
}

// GetKeyPairOfAddr get pubkey and seckey pair of address in specific wallet.
func GetKeyPairOfAddr(walletID string, addr string) (string, error) {
	p, s, err := wallet.GetKeypair(walletID, addr)
	if err != nil {
		return "", err
	}
	var res = struct {
		Pubkey string `json:"pubkey"`
		Seckey string `json:"seckey"`
	}{
		p,
		s,
	}

	d, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(d), nil
}

// GetBalance return balance of a specific address.
func GetBalance(coinType string, address string) (string, error) {
	coin, ok := coinMap[coinType]
	if !ok {
		return "", fmt.Errorf("%s is not supported", coinType)
	}

	if err := coin.ValidateAddr(address); err != nil {
		return "", err
	}

	bal, err := coin.GetBalance([]string{address})
	if err != nil {
		return "", err
	}

	var res = struct {
		Balance uint64 `json:"balance"`
	}{
		bal,
	}

	d, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(d), nil
}

// GetWalletBalance return balance of wallet.
func GetWalletBalance(coinType string, wltID string) (string, error) {
	coin, ok := coinMap[coinType]
	if !ok {
		return "", fmt.Errorf("%s is not supported", coinType)
	}

	addrs, err := wallet.GetAddresses(wltID)
	if err != nil {
		return "", err
	}

	bal, err := coin.GetBalance(addrs)
	if err != nil {
		return "", err
	}
	var res = struct {
		Balance uint64 `json:"balance"`
	}{
		bal,
	}

	d, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(d), nil
}

// SendSky sends skycoins to an address from a specific wallet
func SendSky(walletID string, toAddr string, amount string) (string, error) {
	coin, ok := coinMap["skycoin"]
	if !ok {
		return "", errors.New("skycoin is not supported")
	}

	return coin.Send(walletID, toAddr, amount)
}

// SendMzc sends mzcoin to an address from specific wallet.
func SendMzc(walletID string, toAddr string, amount string) (string, error) {
	coin, ok := coinMap["mzcoin"]
	if !ok {
		return "", errors.New("mzcoin is not supported")
	}

	return coin.Send(walletID, toAddr, amount)
}

// SendBtc sends bitcoins to an address from a specific wallet
func SendBtc(walletID string, toAddr string, amount string, fee string) (string, error) {
	coin, ok := coinMap["bitcoin"]
	if !ok {
		return "", errors.New("bitcoin is not supported")
	}

	return coin.Send(walletID, toAddr, amount, Fee(fee))
}

// GetTransactionByID gets transaction verbose info by id
func GetTransactionByID(coinType, txid string) (string, error) {
	coin, ok := coinMap[coinType]
	if !ok {
		return "", fmt.Errorf("%s is not supported", coinType)
	}

	return coin.GetTransactionByID(txid)
}

// GetOutputByID gets output info by id, Note: bitcoin is not supported.
func GetOutputByID(coinType, id string) (string, error) {
	coin, ok := coinMap[coinType]
	if !ok {
		return "", fmt.Errorf("%s is not supported", coinType)
	}

	return coin.GetOutputByID(id)
}

// ValidateAddress validate the address
func ValidateAddress(coinType, addr string) (bool, error) {
	coin, ok := coinMap[coinType]
	if !ok {
		return false, fmt.Errorf("%s is not supported", coinType)
	}

	if err := coin.ValidateAddr(addr); err != nil {
		return false, err
	}

	return true, nil
}

// NewSeed generates mnemonic seed
func NewSeed() string {
	entropy, err := bip39.NewEntropy(128)
	if err != nil {
		panic(err)
	}

	sd, err := bip39.NewMnemonic(entropy)
	if err != nil {
		panic(err)
	}
	return sd
}

func getPrivateKey(walletID string) coin.GetPrivKey {
	return func(addr string) (string, error) {
		_, s, err := wallet.GetKeypair(walletID, addr)
		return s, err
	}
}
