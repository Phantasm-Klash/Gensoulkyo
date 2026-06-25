package match

import (
	"errors"
	"fmt"
)

const (
	MaxDirectionMask = 15
	MaxCardSlot      = 5
)

var (
	ErrInputPastTick       = errors.New("input tick is older than authoritative tick")
	ErrInputDuplicateTick  = errors.New("input tick is not monotonic")
	ErrInputDuplicateSeq   = errors.New("input sequence is not monotonic")
	ErrInputInvalidDir     = errors.New("input direction mask is invalid")
	ErrInputInvalidCard    = errors.New("input card slot is invalid")
	ErrInputInvalidNeutral = errors.New("input packet uses invalid neutral card slot")
)

type InputPacket struct {
	Tick     int  `json:"tick"`
	Seq      int  `json:"seq"`
	Dir      int  `json:"dir"`
	Slow     bool `json:"slow"`
	Shoot    bool `json:"shoot"`
	Bomb     bool `json:"bomb"`
	CardSlot int  `json:"card_slot"`
}

func NeutralInput(tick int) InputPacket {
	return InputPacket{Tick: tick, CardSlot: -1}
}

type InputGuard struct {
	LastTickByUser map[string]int
	LastSeqByUser  map[string]int
}

func NewInputGuard() InputGuard {
	return InputGuard{
		LastTickByUser: make(map[string]int),
		LastSeqByUser:  make(map[string]int),
	}
}

func (g *InputGuard) Validate(userID string, input InputPacket, authoritativeTick int) error {
	if g.LastTickByUser == nil {
		g.LastTickByUser = make(map[string]int)
	}
	if g.LastSeqByUser == nil {
		g.LastSeqByUser = make(map[string]int)
	}
	if input.Tick < authoritativeTick {
		return fmt.Errorf("%w: got %d current %d", ErrInputPastTick, input.Tick, authoritativeTick)
	}
	if lastTick, ok := g.LastTickByUser[userID]; ok && input.Tick <= lastTick {
		return fmt.Errorf("%w: got %d last %d", ErrInputDuplicateTick, input.Tick, lastTick)
	}
	if lastSeq, ok := g.LastSeqByUser[userID]; ok && input.Seq <= lastSeq {
		return fmt.Errorf("%w: got %d last %d", ErrInputDuplicateSeq, input.Seq, lastSeq)
	}
	if input.Dir < 0 || input.Dir > MaxDirectionMask {
		return fmt.Errorf("%w: %d", ErrInputInvalidDir, input.Dir)
	}
	if input.CardSlot < -1 || input.CardSlot > MaxCardSlot {
		return fmt.Errorf("%w: %d", ErrInputInvalidCard, input.CardSlot)
	}
	return nil
}

func (g *InputGuard) Accept(userID string, input InputPacket) {
	if g.LastTickByUser == nil {
		g.LastTickByUser = make(map[string]int)
	}
	if g.LastSeqByUser == nil {
		g.LastSeqByUser = make(map[string]int)
	}
	g.LastTickByUser[userID] = input.Tick
	g.LastSeqByUser[userID] = input.Seq
}

func IsEmptyInput(input InputPacket) bool {
	return input.Tick == 0 &&
		input.Seq == 0 &&
		input.Dir == 0 &&
		!input.Slow &&
		!input.Shoot &&
		!input.Bomb &&
		input.CardSlot == 0
}

func RepeatableInput(input InputPacket, tick int) InputPacket {
	input.Tick = tick
	input.Bomb = false
	input.CardSlot = -1
	return input
}

type Direction struct {
	X int `json:"x"`
	Y int `json:"y"`
}

func NormalizeDir(mask int) Direction {
	var d Direction
	if mask&1 != 0 {
		d.Y--
	}
	if mask&2 != 0 {
		d.Y++
	}
	if mask&4 != 0 {
		d.X--
	}
	if mask&8 != 0 {
		d.X++
	}
	if d.X != 0 && d.Y != 0 {
		if d.X > 0 {
			d.X = 1
		} else {
			d.X = -1
		}
		if d.Y > 0 {
			d.Y = 1
		} else {
			d.Y = -1
		}
	}
	return d
}
