package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fmt"

	logging "github.com/op/go-logging"
	"github.com/skycoin/skycoin-exchange/src/coin"
	bitcoin "github.com/skycoin/skycoin-exchange/src/coin/bitcoin"
	skycoin "github.com/skycoin/skycoin-exchange/src/coin/skycoin"
	"github.com/skycoin/skycoin-exchange/src/server/account"
	"github.com/skycoin/skycoin-exchange/src/server/engine"
	"github.com/skycoin/skycoin-exchange/src/server/order"
	"github.com/skycoin/skycoin-exchange/src/server/router"
	"github.com/skycoin/skycoin/src/util"
)

var logger = logging.MustGetLogger("exchange.server")

// Config store server's configuration.
type Config struct {
	Server        string            // api server ip
	Port          int               // api port
	BtcFee        int               // btc transaction fee
	DataDir       string            // data directory
	Seed          string            // seed
	Seckey        string            // server's private key
	UtxoPoolSize  int               // utxo pool size.
	Admins        string            // admins joined with `,`
	NodeAddresses map[string]string // node address map
	HttpProf      bool
}

// NewConfig creates config instance and init nodeaddresses map.
func NewConfig() *Config {
	return &Config{NodeAddresses: make(map[string]string)}
}

// ExchangeServer provides services like account system, order book, api for differenct coins, etc.
type ExchangeServer struct {
	account.Manager
	btcum         bitcoin.UtxoManager
	skyum         skycoin.UtxoManager
	orderManager  *order.Manager
	cfg           Config
	wallets       wallets
	wltMtx        sync.RWMutex                // mutex for protecting the wallet.
	orderHandlers map[string]chan order.Order // order handlers, for handleing bid and ask.
	coins         map[string]coin.Gateway
}

// New create new server
func New(cfg *Config) engine.Exchange {
	// init the data dir
	path := initDataDir(cfg.DataDir)

	// init the account dir
	account.InitDir(filepath.Join(path, "account"))

	// init the order book dir.
	order.InitDir(filepath.Join(path, "orderbook"))

	var (
		acntMgr account.Manager
		err     error
	)

	// load account manager if exist.
	acntMgr, err = account.LoadManager()
	if err != nil {
		if os.IsNotExist(err) {
			// create new account manager.
			acntMgr = account.NewManager()
		} else {
			panic(err)
		}
	}

	wltItems := []walletItem{
		{bitcoin.Type, cfg.Seed},
		{skycoin.Type, cfg.Seed},
	}

	// init wallets in server.
	wlts, err := makeWallets(filepath.Join(path, "wallet"), wltItems)
	if err != nil {
		panic(err)
	}

	// create bitcoin utxo manager
	btcWatchAddrs, err := wlts.GetAddresses(bitcoin.Type)
	if err != nil {
		panic(err)
	}
	btcum := bitcoin.NewUtxoManager(cfg.UtxoPoolSize, btcWatchAddrs)

	// create skycoin utxo manager
	skyWatchAddrs, err := wlts.GetAddresses(skycoin.Type)
	if err != nil {
		panic(err)
	}
	skyum := skycoin.NewUtxoManager(cfg.NodeAddresses[skycoin.Type], cfg.UtxoPoolSize, skyWatchAddrs)

	// load or create order books.
	var orderManager *order.Manager
	orderManager, err = order.LoadManager()
	if err != nil {
		if os.IsNotExist(err) {
			orderManager = order.NewManager()
			orderManager.AddBook("bitcoin/skycoin", &order.Book{})
		} else {
			panic(err)
		}
	}

	s := &ExchangeServer{
		cfg:          *cfg,
		wallets:      wlts,
		Manager:      acntMgr,
		btcum:        btcum,
		skyum:        skyum,
		orderManager: orderManager,
		coins:        make(map[string]coin.Gateway),
		orderHandlers: map[string]chan order.Order{
			"bitcoin/skycoin": make(chan order.Order, 100),
		},
	}

	return s
}

// BindCoins registers coins
func (serv *ExchangeServer) BindCoins(cs ...coin.Gateway) error {
	for _, c := range cs {
		if _, exist := serv.coins[c.Type()]; exist {
			return fmt.Errorf("%s coin already registered", c.Type())
		}
		serv.coins[c.Type()] = c
	}

	return nil
}

// Run start the exchange server.
func (self *ExchangeServer) Run() {
	logger.Info("server started %s:%d", self.cfg.Server, self.cfg.Port)
	// register coins
	// coin.RegisterGateway(coin.Bitcoin, &bitcoin.GatewayIns)
	// coin.RegisterGateway(coin.Skycoin, &skycoin.GatewayIns)
	// init the skycoin node address.
	// skycoin.ServeAddr = self.cfg.SkycoinNodeAddr

	// register the order handlers
	for cp, c := range self.orderHandlers {
		self.orderManager.RegisterOrderChan(cp, c)
	}

	// start the utxo manager
	c := make(chan bool)
	go self.btcum.Start(c)
	go self.skyum.Start(c)

	go self.orderManager.Start(1*time.Second, c)
	self.handleOrders(c)

	// start the api server.
	// r := NewRouter(self)
	r := router.New(self, c)
	r.Run(self.cfg.Server, self.cfg.Port)
}

// GetBtcFee get transaction fee of bitcoin.
func (self ExchangeServer) GetBtcFee() uint64 {
	return uint64(self.cfg.BtcFee)
}

func (self ExchangeServer) GetSecKey() string {
	return self.cfg.Seckey
}

// GetPrivKey get the private key of specific address.
func (self ExchangeServer) GetAddrPrivKey(cp, addr string) (string, error) {
	_, key, err := self.wallets.GetKeypair(cp, addr)
	if err != nil {
		return "", err
	}

	return key, nil
}

// GetNewAddress create new address of specific coin type.
func (self *ExchangeServer) GetNewAddress(cp string) string {
	self.wltMtx.Lock()
	defer self.wltMtx.Unlock()
	addrEntry, err := self.wallets.NewAddresses(cp, 1)
	if err != nil {
		panic("server get new address failed")
	}
	return addrEntry[0].Address
}

// GetCoin gets coin gateway of specific type.
func (serv *ExchangeServer) GetCoin(ct string) (coin.Gateway, error) {
	c, ok := serv.coins[ct]
	if !ok {
		return nil, fmt.Errorf("%s coin is not supported", ct)
	}
	return c, nil
}

// ChooseUtxos choose appropriate bitcoin utxos,
func (self *ExchangeServer) ChooseUtxos(cp string, amount uint64, tm time.Duration) (interface{}, error) {
	switch cp {
	case bitcoin.Type:
		return self.btcum.ChooseUtxos(amount, tm)
	case skycoin.Type:
		return self.skyum.ChooseUtxos(amount, tm)
	default:
		return nil, errors.New("unknow coin type")
	}
}

// PutUtxos set back the utxos of specific coin type.
func (self *ExchangeServer) PutUtxos(cp string, utxos interface{}) {
	switch cp {
	case bitcoin.Type:
		btcUtxos := utxos.([]bitcoin.Utxo)
		for _, u := range btcUtxos {
			self.btcum.PutUtxo(u)
		}
	case skycoin.Type:
		skyUtxos := utxos.([]skycoin.Utxo)
		for _, u := range skyUtxos {
			self.skyum.PutUtxo(u)
		}
	}
}

// AddWatchAddress add watch address to utxo manager.
func (self *ExchangeServer) WatchAddress(cp, addr string) {
	switch cp {
	case bitcoin.Type:
		self.btcum.WatchAddresses([]string{addr})
	case skycoin.Type:
		self.skyum.WatchAddresses([]string{addr})
	}
}

func (self *ExchangeServer) SaveAccount() error {
	return self.Save()
}

func (self *ExchangeServer) AddOrder(cp string, odr order.Order) (uint64, error) {
	return self.orderManager.AddOrder(cp, odr)
}

func (self ExchangeServer) IsAdmin(pubkey string) bool {
	logger.Debug("admins:%s, pubkey:%s", self.cfg.Admins, pubkey)
	return strings.Contains(self.cfg.Admins, pubkey)
}

// initDataDir init the data dir of skycoin exchange.
func initDataDir(dir string) string {
	if dir == "" {
		logger.Error("data directory is nil")
	}

	home := util.UserHome()
	if home == "" {
		logger.Warning("Failed to get home directory")
		dir = filepath.Join("./", dir)
	} else {
		dir = filepath.Join(home, dir)
	}

	if err := os.MkdirAll(dir, os.FileMode(0700)); err != nil {
		logger.Error("Failed to create directory %s: %v", dir, err)
	}
	return dir
}

func (self *ExchangeServer) handleOrders(c chan bool) {
	for cp, ch := range self.orderHandlers {
		go func(cp string, ch chan order.Order, closing chan bool) {
			for {
				select {
				case <-closing:
					return
				case order := <-ch:
					// handle the order
					self.settleOrder(cp, order)
				}
			}
		}(cp, ch, c)
	}
}

func (self *ExchangeServer) settleOrder(cp string, od order.Order) {
	logger.Info("match order=== type:%s, price:%d, amount:%d", od.Type, od.Price, od.Amount)
	acnt, err := self.GetAccount(od.AccountID)
	if err != nil {
		panic("error account id")
	}

	pair := strings.Split(cp, "/")
	if len(pair) != 2 {
		panic("error coin pair")
	}
	mainCt := pair[0]
	subCt := pair[1]

	switch od.Type {
	case order.Bid:
		// increase main coin balance
		logger.Info("account:%s increase %s:%d", od.AccountID, mainCt, od.Amount)
		if err := acnt.IncreaseBalance(mainCt, od.Amount); err != nil {
			panic(err)
		}

		self.SaveAccount()
	case order.Ask:
		// increase sub coin balance.
		logger.Info("account:%s increase %s:%d", od.AccountID, subCt, od.Price*od.Amount)
		if err := acnt.IncreaseBalance(subCt, od.Price*od.Amount); err != nil {
			panic(err)
		}
		// decrease main coin balance.
		logger.Info("account:%s decrease %s:%d", od.AccountID, mainCt, od.Amount)
		if err := acnt.DecreaseBalance(mainCt, od.Amount); err != nil {
			panic(err)
		}
		self.SaveAccount()
	}
}

func (self *ExchangeServer) GetOrders(cp string, tp order.Type, start, end int64) ([]order.Order, error) {
	return self.orderManager.GetOrders(cp, tp, start, end)
}

// GetSupportCoins returns all supported coin's symbol
func (serv *ExchangeServer) GetSupportCoins() []string {
	symbols := make([]string, len(serv.coins))
	i := 0
	for _, coin := range serv.coins {
		symbols[i] = coin.Symbol()
		i++
	}
	return symbols
}
