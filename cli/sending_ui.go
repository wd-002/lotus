package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Kubuxu/imtui"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/lotus/api"
	types "github.com/filecoin-project/lotus/chain/types"
	"github.com/gdamore/tcell/v2"
	cid "github.com/ipfs/go-cid"
)

var interactiveSolves = map[api.CheckStatusCode]bool{
	api.CheckStatusMessageBaseFee:           true,
	api.CheckStatusMessageBaseFeeLowerBound: true,
	api.CheckStatusMessageBaseFeeUpperBound: true,
}

func baseFeeFromHints(hint map[string]interface{}) big.Int {
	bHint, ok := hint["baseFee"]
	if !ok {
		return big.Zero()
	}
	bHintS, ok := bHint.(string)
	if !ok {
		return big.Zero()
	}

	var err error
	baseFee, err := big.FromString(bHintS)
	if err != nil {
		return big.Zero()
	}
	return baseFee
}

func resolveChecks(ctx context.Context, s ServicesAPI, printer io.Writer,
	proto *types.Message, checkGroups [][]api.MessageCheckStatus,
	interactive bool) (*types.Message, error) {

	fmt.Fprintf(printer, "Following checks have failed:\n")
	printChecks(printer, checkGroups, proto.Cid())
	if !interactive {
		return nil, ErrCheckFailed
	}

	if interactive {
		if feeCapBad, baseFee := isFeeCapProblem(checkGroups, proto.Cid()); feeCapBad {
			fmt.Fprintf(printer, "Fee of the message can be adjusted\n")
			if askUser(printer, "Do you wish to do that? [Yes/no]: ", true) {
				var err error
				proto, err = runFeeCapAdjustmentUI(proto, baseFee)
				if err != nil {
					return nil, err
				}
			}
			checks, err := s.RunChecksForPrototype(ctx, proto)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(printer, "Following checks still failed:\n")
			printChecks(printer, checks, proto.Cid())
		}

		if !askUser(printer, "Do you wish to send this message? [yes/No]: ", false) {
			return nil, ErrAbortedByUser
		}
	}
	return proto, nil
}

var ErrAbortedByUser = errors.New("aborted by user")

func printChecks(printer io.Writer, checkGroups [][]api.MessageCheckStatus, protoCid cid.Cid) {
	for _, checks := range checkGroups {
		for _, c := range checks {
			if c.OK {
				continue
			}
			aboutProto := c.Cid.Equals(protoCid)
			msgName := "current"
			if !aboutProto {
				msgName = c.Cid.String()
			}
			fmt.Fprintf(printer, "%s message failed a check: %s\n", msgName, c.Err)
		}
	}
}

func askUser(printer io.Writer, q string, def bool) bool {
	var resp string
	fmt.Fprint(printer, q)
	fmt.Scanln(&resp)
	resp = strings.ToLower(resp)
	if len(resp) == 0 {
		return def
	}
	return resp[0] == 'y'
}

func isFeeCapProblem(checkGroups [][]api.MessageCheckStatus, protoCid cid.Cid) (bool, big.Int) {
	baseFee := big.Zero()
	yes := false
	for _, checks := range checkGroups {
		for _, c := range checks {
			if c.OK {
				continue
			}
			aboutProto := c.Cid.Equals(protoCid)
			if aboutProto && interactiveSolves[c.Code] {
				yes = true
				if baseFee.IsZero() {
					baseFee = baseFeeFromHints(c.Hint)
				}
			}
		}
	}

	return yes, baseFee
}

func runFeeCapAdjustmentUI(proto *types.Message, baseFee abi.TokenAmount) (*types.Message, error) {
	t, err := imtui.NewTui()
	if err != nil {
		return nil, err
	}

	maxFee := big.Mul(proto.GasFeeCap, big.NewInt(proto.GasLimit))
	send := false
	t.SetScene(ui(baseFee, proto.GasLimit, &maxFee, &send))

	err = t.Run()
	if err != nil {
		return nil, err
	}
	if !send {
		return nil, fmt.Errorf("aborted by user")
	}

	proto.GasFeeCap = big.Div(maxFee, big.NewInt(proto.GasLimit))

	return proto, nil
}

func ui(baseFee abi.TokenAmount, gasLimit int64, maxFee *abi.TokenAmount, send *bool) func(*imtui.Tui) error {
	orignalMaxFee := *maxFee
	required := big.Mul(baseFee, big.NewInt(gasLimit))
	safe := big.Mul(required, big.NewInt(10))

	price := fmt.Sprintf("%s", types.FIL(*maxFee).Unitless())

	return func(t *imtui.Tui) error {
		if t.CurrentKey != nil {
			if t.CurrentKey.Key() == tcell.KeyRune {
				pF, err := types.ParseFIL(price)
				switch t.CurrentKey.Rune() {
				case 's', 'S':
					price = types.FIL(safe).Unitless()
				case '+':
					if err == nil {
						p := big.Mul(big.Int(pF), types.NewInt(11))
						p = big.Div(p, types.NewInt(10))
						price = fmt.Sprintf("%s", types.FIL(p).Unitless())
					}
				case '-':
					if err == nil {
						p := big.Mul(big.Int(pF), types.NewInt(10))
						p = big.Div(p, types.NewInt(11))
						price = fmt.Sprintf("%s", types.FIL(p).Unitless())
					}
				default:
				}
			}

			if t.CurrentKey.Key() == tcell.KeyEnter {
				*send = true
				return imtui.ErrNormalExit
			}
		}

		defS := tcell.StyleDefault

		row := 0
		t.Label(0, row, "Fee of the message is too low.", defS)
		row++

		t.Label(0, row, fmt.Sprintf("Your configured maximum fee is: %s FIL",
			types.FIL(orignalMaxFee).Unitless()), defS)
		row++
		t.Label(0, row, fmt.Sprintf("Required maximum fee for the message: %s FIL",
			types.FIL(required).Unitless()), defS)
		row++
		w := t.Label(0, row, fmt.Sprintf("Safe maximum fee for the message: %s FIL",
			types.FIL(safe).Unitless()), defS)
		t.Label(w, row, "   Press S to use it", defS)
		row++

		w = t.Label(0, row, "Current Maximum Fee: ", defS)

		w += t.EditFieldFiltered(w, row, 14, &price, imtui.FilterDecimal, defS.Foreground(tcell.ColorWhite).Background(tcell.ColorBlack))

		w += t.Label(w, row, " FIL", defS)

		pF, err := types.ParseFIL(price)
		*maxFee = abi.TokenAmount(pF)
		if err != nil {
			w += t.Label(w, row, " invalid price", defS.Foreground(tcell.ColorMaroon).Bold(true))
		} else if maxFee.GreaterThanEqual(safe) {
			w += t.Label(w, row, " SAFE", defS.Foreground(tcell.ColorDarkGreen).Bold(true))
		} else if maxFee.GreaterThanEqual(required) {
			w += t.Label(w, row, " low", defS.Foreground(tcell.ColorYellow).Bold(true))
			over := big.Div(big.Mul(*maxFee, big.NewInt(100)), required)
			w += t.Label(w, row,
				fmt.Sprintf(" %.1fx over the minimum", float64(over.Int64())/100.0), defS)
		} else {
			w += t.Label(w, row, " too low", defS.Foreground(tcell.ColorRed).Bold(true))
		}
		row += 2

		t.Label(0, row, fmt.Sprintf("Current Base Fee is: %s", types.FIL(baseFee)), defS)
		row++
		t.Label(0, row, fmt.Sprintf("Resulting FeeCap is: %s",
			types.FIL(big.Div(*maxFee, big.NewInt(gasLimit)))), defS)
		row++
		t.Label(0, row, "You can use '+' and '-' to adjust the fee.", defS)

		return nil
	}
}
