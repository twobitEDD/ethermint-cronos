// Copyright 2021 Evmos Foundation
// This file is part of Evmos' Ethermint library.
//
// The Ethermint library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The Ethermint library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the Ethermint library. If not, see https://github.com/evmos/ethermint/blob/main/LICENSE
package types

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	sdkmath "cosmossdk.io/math"

	errorsmod "cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/client"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	"github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

var (
	_ sdk.Msg    = &MsgEthereumTx{}
	_ sdk.Tx     = &MsgEthereumTx{}
	_ ante.GasTx = &MsgEthereumTx{}
	_ sdk.Msg    = &MsgUpdateParams{}

	_ codectypes.UnpackInterfacesMessage = MsgEthereumTx{}
)

// message type and route constants
const (
	// TypeMsgEthereumTx defines the type string of an Ethereum transaction
	TypeMsgEthereumTx = "ethereum_tx"
)

// NewTx returns a reference to a new Ethereum transaction message.
func NewTx(
	chainID *big.Int, nonce uint64, to *common.Address, amount *big.Int,
	gasLimit uint64, gasPrice, gasFeeCap, gasTipCap *big.Int, input []byte, accesses *ethtypes.AccessList,
) *MsgEthereumTx {
	return newMsgEthereumTx(chainID, nonce, to, amount, gasLimit, gasPrice, gasFeeCap, gasTipCap, input, accesses)
}

// NewTxContract returns a reference to a new Ethereum transaction
// message designated for contract creation.
func NewTxContract(
	chainID *big.Int,
	nonce uint64,
	amount *big.Int,
	gasLimit uint64,
	gasPrice, gasFeeCap, gasTipCap *big.Int,
	input []byte,
	accesses *ethtypes.AccessList,
) *MsgEthereumTx {
	return newMsgEthereumTx(chainID, nonce, nil, amount, gasLimit, gasPrice, gasFeeCap, gasTipCap, input, accesses)
}

func newMsgEthereumTx(
	chainID *big.Int, nonce uint64, to *common.Address, amount *big.Int,
	gasLimit uint64, gasPrice, gasFeeCap, gasTipCap *big.Int, input []byte, accesses *ethtypes.AccessList,
) *MsgEthereumTx {
	var (
		cid, amt, gp *sdkmath.Int
		toAddr       string
		txData       TxData
	)

	if to != nil {
		toAddr = to.Hex()
	}

	if amount != nil {
		amountInt := sdkmath.NewIntFromBigInt(amount)
		amt = &amountInt
	}

	if chainID != nil {
		chainIDInt := sdkmath.NewIntFromBigInt(chainID)
		cid = &chainIDInt
	}

	if gasPrice != nil {
		gasPriceInt := sdkmath.NewIntFromBigInt(gasPrice)
		gp = &gasPriceInt
	}

	switch {
	case accesses == nil:
		txData = &LegacyTx{
			Nonce:    nonce,
			To:       toAddr,
			Amount:   amt,
			GasLimit: gasLimit,
			GasPrice: gp,
			Data:     input,
		}
	case accesses != nil && gasFeeCap != nil && gasTipCap != nil:
		gtc := sdkmath.NewIntFromBigInt(gasTipCap)
		gfc := sdkmath.NewIntFromBigInt(gasFeeCap)

		txData = &DynamicFeeTx{
			ChainID:   cid,
			Nonce:     nonce,
			To:        toAddr,
			Amount:    amt,
			GasLimit:  gasLimit,
			GasTipCap: &gtc,
			GasFeeCap: &gfc,
			Data:      input,
			Accesses:  NewAccessList(accesses),
		}
	case accesses != nil:
		txData = &AccessListTx{
			ChainID:  cid,
			Nonce:    nonce,
			To:       toAddr,
			Amount:   amt,
			GasLimit: gasLimit,
			GasPrice: gp,
			Data:     input,
			Accesses: NewAccessList(accesses),
		}
	default:
	}

	dataAny, err := PackTxData(txData)
	if err != nil {
		panic(err)
	}

	msg := MsgEthereumTx{Data: dataAny}
	msg.Hash = msg.AsTransaction().Hash().Hex()
	return &msg
}

// FromEthereumTx populates the message fields from the given ethereum transaction
func (msg *MsgEthereumTx) FromEthereumTx(tx *ethtypes.Transaction) error {
	txData, err := NewTxDataFromTx(tx)
	if err != nil {
		return err
	}

	anyTxData, err := PackTxData(txData)
	if err != nil {
		return err
	}

	msg.Data = anyTxData
	msg.Hash = tx.Hash().Hex()
	return nil
}

// FromSignedEthereumTx populates the message fields from the given signed ethereum transaction, and set From field.
func (msg *MsgEthereumTx) FromSignedEthereumTx(tx *ethtypes.Transaction, chainID *big.Int) error {
	if err := msg.FromEthereumTx(tx); err != nil {
		return err
	}

	from, err := msg.recoverSender(chainID)
	if err != nil {
		return err
	}

	msg.From = from.Bytes()
	return nil
}

// Route returns the route value of an MsgEthereumTx.
func (msg MsgEthereumTx) Route() string { return RouterKey }

// Type returns the type value of an MsgEthereumTx.
func (msg MsgEthereumTx) Type() string { return TypeMsgEthereumTx }

// ValidateBasic implements the sdk.Msg interface. It performs basic validation
// checks of a Transaction. If returns an error if validation fails.
func (msg MsgEthereumTx) ValidateBasic() error {
	if len(msg.DeprecatedFrom) != 0 {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "deprecated From field is not empty")
	}

	if len(msg.From) == 0 {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "sender address is missing")
	}

	// Validate Size_ field, should be kept empty
	if msg.Size_ != 0 {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "tx size is deprecated")
	}

	txData, err := UnpackTxData(msg.Data)
	if err != nil {
		return errorsmod.Wrap(err, "failed to unpack tx data")
	}

	// prevent txs with 0 gas to fill up the mempool
	if txData.GetGas() == 0 {
		return errorsmod.Wrap(ErrInvalidGasLimit, "gas limit must not be zero")
	}

	if err := txData.Validate(); err != nil {
		return err
	}

	// Validate Hash field after validated txData to avoid panic
	txHash := msg.AsTransaction().Hash().Hex()
	if msg.Hash != txHash {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "invalid tx hash %s, expected: %s", msg.Hash, txHash)
	}

	return nil
}

// GetMsgs returns a single MsgEthereumTx as an sdk.Msg.
func (msg *MsgEthereumTx) GetMsgs() []sdk.Msg {
	return []sdk.Msg{msg}
}

// GetSigners returns the expected signers for an Ethereum transaction message.
// For such a message, there should exist only a single 'signer'.
func (msg *MsgEthereumTx) GetSigners() []sdk.AccAddress {
	if len(msg.From) == 0 {
		return nil
	}
	return []sdk.AccAddress{msg.GetFrom()}
}

// GetSender convert the From field to common.Address
// From should always be set, which is validated in ValidateBasic
func (msg *MsgEthereumTx) GetSender() common.Address {
	return common.BytesToAddress(msg.From)
}

// GetSenderLegacy fallbacks to old behavior if From is empty, should be used by json-rpc
func (msg *MsgEthereumTx) GetSenderLegacy(chainID *big.Int) (common.Address, error) {
	if len(msg.From) > 0 {
		return msg.GetSender(), nil
	}
	sender, err := msg.recoverSender(chainID)
	if err != nil {
		return common.Address{}, err
	}
	msg.From = sender.Bytes()
	return sender, nil
}

// recoverSender recovers the sender address from the transaction signature.
func (msg *MsgEthereumTx) recoverSender(chainID *big.Int) (common.Address, error) {
	return ethtypes.LatestSignerForChainID(chainID).Sender(msg.AsTransaction())
}

// GetSignBytes returns the Amino bytes of an Ethereum transaction message used
// for signing.
//
// NOTE: This method cannot be used as a chain ID is needed to create valid bytes
// to sign over. Use 'RLPSignBytes' instead.
func (msg MsgEthereumTx) GetSignBytes() []byte {
	panic("must use 'RLPSignBytes' with a chain ID to get the valid bytes to sign")
}

// Sign calculates a secp256k1 ECDSA signature and signs the transaction. It
// takes a keyring signer and the chainID to sign an Ethereum transaction according to
// EIP155 standard.
// This method mutates the transaction as it populates the V, R, S
// fields of the Transaction's Signature.
// The function will fail if the sender address is not defined for the msg or if
// the sender is not registered on the keyring
func (msg *MsgEthereumTx) Sign(ethSigner ethtypes.Signer, keyringSigner keyring.Signer) error {
	from := msg.GetFrom()
	if from.Empty() {
		return fmt.Errorf("sender address not defined for message")
	}

	tx := msg.AsTransaction()
	txHash := ethSigner.Hash(tx)

	sig, _, err := keyringSigner.SignByAddress(from, txHash.Bytes())
	if err != nil {
		return err
	}

	tx, err = tx.WithSignature(ethSigner, sig)
	if err != nil {
		return err
	}

	return msg.FromEthereumTx(tx)
}

// GetGas implements the GasTx interface. It returns the GasLimit of the transaction.
func (msg MsgEthereumTx) GetGas() uint64 {
	txData, err := UnpackTxData(msg.Data)
	if err != nil {
		return 0
	}
	return txData.GetGas()
}

// GetFee returns the fee for non dynamic fee tx
func (msg MsgEthereumTx) GetFee() *big.Int {
	txData, err := UnpackTxData(msg.Data)
	if err != nil {
		return nil
	}
	return txData.Fee()
}

// GetEffectiveFee returns the fee for dynamic fee tx
func (msg MsgEthereumTx) GetEffectiveFee(baseFee *big.Int) *big.Int {
	txData, err := UnpackTxData(msg.Data)
	if err != nil {
		return nil
	}
	return txData.EffectiveFee(baseFee)
}

// GetFrom loads the ethereum sender address from the sigcache and returns an
// sdk.AccAddress from its bytes
func (msg *MsgEthereumTx) GetFrom() sdk.AccAddress {
	return sdk.AccAddress(msg.From)
}

// AsTransaction creates an Ethereum Transaction type from the msg fields
func (msg MsgEthereumTx) AsTransaction() *ethtypes.Transaction {
	txData, err := UnpackTxData(msg.Data)
	if err != nil {
		return nil
	}

	return ethtypes.NewTx(txData.AsEthereumData())
}

// AsMessage creates an Ethereum core.Message from the msg fields
func (msg MsgEthereumTx) AsMessage(baseFee *big.Int) (core.Message, error) {
	txData, err := UnpackTxData(msg.Data)
	if err != nil {
		return nil, err
	}

	gasPrice, gasFeeCap, gasTipCap := txData.GetGasPrice(), txData.GetGasFeeCap(), txData.GetGasTipCap()
	if baseFee != nil {
		gasPrice = math.BigMin(gasPrice.Add(gasTipCap, baseFee), gasFeeCap)
	}
	ethMsg := ethtypes.NewMessage(
		msg.GetSender(),
		txData.GetTo(),
		txData.GetNonce(),
		txData.GetValue(),
		txData.GetGas(),
		gasPrice, gasFeeCap, gasTipCap,
		txData.GetData(),
		txData.GetAccessList(),
		false,
	)

	return ethMsg, nil
}

// VerifySender verify the sender address against the signature values using the latest signer for the given chainID.
func (msg *MsgEthereumTx) VerifySender(chainID *big.Int) error {
	from, err := msg.recoverSender(chainID)
	if err != nil {
		return err
	}

	if !bytes.Equal(msg.From, from.Bytes()) {
		return fmt.Errorf("sender verification failed. got %s, expected %s", from.String(), HexAddress(msg.From))
	}
	return nil
}

// UnpackInterfaces implements UnpackInterfacesMesssage.UnpackInterfaces
func (msg MsgEthereumTx) UnpackInterfaces(unpacker codectypes.AnyUnpacker) error {
	return unpacker.UnpackAny(msg.Data, new(TxData))
}

// UnmarshalBinary decodes the canonical encoding of transactions.
func (msg *MsgEthereumTx) UnmarshalBinary(b []byte, chainID *big.Int) error {
	tx := &ethtypes.Transaction{}
	if err := tx.UnmarshalBinary(b); err != nil {
		return err
	}
	return msg.FromSignedEthereumTx(tx, chainID)
}

// BuildTx builds the canonical cosmos tx from ethereum msg
func (msg *MsgEthereumTx) BuildTx(b client.TxBuilder, evmDenom string) (signing.Tx, error) {
	builder, ok := b.(authtx.ExtensionOptionsTxBuilder)
	if !ok {
		return nil, errors.New("unsupported builder")
	}

	option, err := codectypes.NewAnyWithValue(&ExtensionOptionsEthereumTx{})
	if err != nil {
		return nil, err
	}

	txData, err := UnpackTxData(msg.Data)
	if err != nil {
		return nil, err
	}
	fees := make(sdk.Coins, 0)
	feeAmt := sdkmath.NewIntFromBigInt(txData.Fee())
	if feeAmt.Sign() > 0 {
		fees = append(fees, sdk.NewCoin(evmDenom, feeAmt))
	}

	builder.SetExtensionOptions(option)

	err = builder.SetMsgs(msg)
	if err != nil {
		return nil, err
	}
	builder.SetFeeAmount(fees)
	builder.SetGasLimit(msg.GetGas())
	tx := builder.GetTx()
	return tx, nil
}

// GetSigners returns the expected signers for a MsgUpdateParams message.
func (m MsgUpdateParams) GetSigners() []sdk.AccAddress {
	//#nosec G703 -- gosec raises a warning about a non-handled error which we deliberately ignore here
	addr, _ := sdk.AccAddressFromBech32(m.Authority)
	return []sdk.AccAddress{addr}
}

// ValidateBasic does a sanity check of the provided data
func (m *MsgUpdateParams) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Authority); err != nil {
		return errorsmod.Wrap(err, "invalid authority address")
	}

	return m.Params.Validate()
}

// GetSignBytes implements the LegacyMsg interface.
func (m MsgUpdateParams) GetSignBytes() []byte {
	return sdk.MustSortJSON(AminoCdc.MustMarshalJSON(&m))
}
