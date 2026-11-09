package main

// Typing sounds for the search bar, generated in code (no sound files).
// Each style has a few slightly different versions that rotate while typing.

import (
	"encoding/binary"
	"math"
)

const synthRate = 44100

type clickStyle struct {
	ID   string
	Name string
	Desc string
	gen  func(t float64, nz *noiseGen, pitch float64) float64
	secs float64
	vol  float64
}

type noiseGen struct {
	seed         uint32
	prev, hp, lp float64
	bq           [16][4]float64 // band-pass filter states (x1, x2, y1, y2)
}

// band-pass resonance on white noise: how a key's plastic and plate "ring" for a moment
func (n *noiseGen) bp(k int, f, q float64) float64 {
	w0 := 2 * math.Pi * f / synthRate
	alpha := math.Sin(w0) / (2 * q)
	a0 := 1 + alpha
	b0, b2 := alpha/a0, -alpha/a0
	a1, a2 := -2*math.Cos(w0)/a0, (1-alpha)/a0
	x := n.white()
	st := &n.bq[k]
	y := b0*x + b2*st[1] - a1*st[2] - a2*st[3]
	st[1], st[0] = st[0], x
	st[3], st[2] = st[2], y
	return y
}

func softAttack(t, ms float64) float64 { return math.Min(1, t/(ms/1000)) }

func (n *noiseGen) white() float64 {
	n.seed = n.seed*1664525 + 1013904223
	return float64(int32(n.seed)) / math.MaxInt32
}

// crisp (high-passed) and soft (low-passed) noise
func (n *noiseGen) bright() float64 {
	w := n.white()
	n.hp = 0.86 * (n.hp + w - n.prev)
	n.prev = w
	return n.hp
}

func (n *noiseGen) dull(a float64) float64 {
	n.lp += a * (n.white() - n.lp)
	return n.lp
}

func sine(f, t float64) float64 { return math.Sin(2 * math.Pi * f * t) }
func decay(t, tau float64) float64 {
	if t < 0 {
		return 0
	}
	return math.Exp(-t / tau)
}

// creamy: soft, rounded, no sharp click or ping, short muffled finish
func creamyKey(t float64, nz *noiseGen, p, body, pop float64) float64 {
	return 1.0*nz.bp(0, pop*p, 3)*decay(t, 0.006)*softAttack(t, 1.5) +
		0.5*nz.bp(1, body*p, 2)*decay(t, 0.012)*softAttack(t, 2) +
		0.25*sine(170*p, t)*decay(t, 0.014)
}

// clean click: modelled on a reference "clean mechanical tap" (press + quieter
// release 86 ms later), built from the same resonances, scaled by k to pitch it.
func cleanPress(t float64, nz *noiseGen, k float64) float64 {
	a := softAttack(t, 2.2)
	return a*(1.15*nz.bp(0, 1722*k, 8)*decay(t, 0.004)+
		0.8*nz.bp(1, 1426*k, 8)*decay(t, 0.004)+
		0.75*nz.bp(2, 645*k, 5)*decay(t, 0.005)+
		0.25*nz.bp(11, 759*k, 6)*decay(t, 0.005)+
		0.32*nz.bp(3, 2761*k, 6)*decay(t, 0.003)+
		0.30*nz.bp(10, 4274*k, 5)*decay(t, 0.0025)+
		1.0*nz.bp(4, 5211*k, 3)*decay(t, 0.002)+
		0.3*nz.bright()*decay(t, 0.0015)) +
		0.12*nz.bp(5, 1722*k, 15)*decay(t, 0.009) // the faint tail
}

func cleanRelease(t float64, nz *noiseGen, k float64) float64 {
	return softAttack(t, 1.5) * (0.6*nz.bp(6, 450*k, 4) + 0.5*nz.bp(7, 662*k, 5) + 0.6*nz.bp(8, 1448*k, 8) +
		0.5*nz.bp(9, 1722*k, 8) + 0.25*nz.bp(12, 5211*k, 4)) * decay(t, 0.004)
}

func cleanClick(semitones float64, release bool) func(float64, *noiseGen, float64) float64 {
	k := math.Pow(2, semitones/12)
	return func(t float64, nz *noiseGen, p float64) float64 {
		v := cleanPress(t, nz, k*p)
		if t2 := t - 0.086; release && t2 >= 0 {
			v += 0.58 * cleanRelease(t2, nz, k*p)
		}
		return v
	}
}

var clickStyles = []clickStyle{
	{ID: "clean0", Name: "Clean click (original pitch)", Desc: "Rebuilt from your sound, at its original pitch.", secs: 0.13, vol: 0.32, gen: cleanClick(0, true)},
	{ID: "clean2", Name: "Clean click, a little higher", Desc: "Your sound pitched up 2 semitones.", secs: 0.13, vol: 0.32, gen: cleanClick(2, true)},
	{ID: "clean3", Name: "Clean click, higher", Desc: "Pitched up 3 semitones.", secs: 0.13, vol: 0.32, gen: cleanClick(3, true)},
	{ID: "clean4", Name: "Clean click, higher still", Desc: "Pitched up 4 semitones.", secs: 0.13, vol: 0.32, gen: cleanClick(4, true)},
	{ID: "clean3p", Name: "Clean click, higher, press only", Desc: "Pitched up 3 semitones, without the release click. Tighter for fast typing.", secs: 0.045, vol: 0.32, gen: cleanClick(3, false)},
	{ID: "softclicky", Name: "Soft clicky", Desc: "The clicky sound with the ping toned down and the edge rounded off.", secs: 0.05, vol: 0.30,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			v := 0.9*nz.bp(0, 2600*p, 2)*decay(t, 0.0016)*softAttack(t, 0.4) + // the click
				0.10*sine(3000*p, t)*decay(t, 0.002) + 0.08*sine(2200*p, t)*decay(t, 0.0025) // a hint of ping
			if t2 := t - 0.011; t2 >= 0 { // bottoming out, soft
				v += 0.45*nz.bp(1, 700*p, 2.5)*decay(t2, 0.005)*softAttack(t2, 1) + 0.3*sine(220*p, t2)*decay(t2, 0.011)
			}
			return v
		}},
	{ID: "mutedclicky", Name: "Muted clicky", Desc: "A clear click with no ping at all, like a dampened clicky switch.", secs: 0.045, vol: 0.30,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			v := 0.9 * nz.bp(0, 1800*p, 2.5) * decay(t, 0.002) * softAttack(t, 0.5)
			if t2 := t - 0.010; t2 >= 0 {
				v += 0.4*nz.bp(1, 600*p, 2)*decay(t2, 0.005)*softAttack(t2, 1) + 0.3*sine(210*p, t2)*decay(t2, 0.01)
			}
			return v
		}},
	{ID: "tactile", Name: "Tactile click", Desc: "A crisp, lower tick with a little pop. Clicky, but easy on the ears.", secs: 0.045, vol: 0.30,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			return 0.85*nz.bp(0, 1400*p, 4)*decay(t, 0.0025)*softAttack(t, 0.5) +
				0.25*sine(1100*p, t)*decay(t, 0.004) +
				0.35*nz.bp(1, 450*p, 2)*decay(t, 0.008)*softAttack(t, 1.5) + 0.2*sine(190*p, t)*decay(t, 0.012)
		}},
	{ID: "clickycreamy", Name: "Clicky on creamy", Desc: "A light, quick click on top of a smooth, creamy keystroke.", secs: 0.05, vol: 0.32,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			return 0.55*nz.bp(2, 3000*p, 3)*decay(t, 0.0009)*softAttack(t, 0.3) + 0.8*creamyKey(t, nz, p, 300, 750)
		}},
	{ID: "creamy", Name: "Creamy", Desc: "Soft, rounded pop with a muffled finish. Smooth, lubed keys.", secs: 0.05, vol: 0.36,
		gen: func(t float64, nz *noiseGen, p float64) float64 { return creamyKey(t, nz, p, 300, 750) }},
	{ID: "creamypop", Name: "Creamy & poppy", Desc: "A little more pop in the middle, still smooth and quiet.", secs: 0.045, vol: 0.36,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			f0, f1, tau := 1000*p, 600*p, 0.004 // a quick pitch drop gives the "pop"
			phase := 2 * math.Pi * (f1*t + (f0-f1)*tau*(1-math.Exp(-t/tau)))
			return 0.5*math.Sin(phase)*decay(t, 0.012)*softAttack(t, 1) +
				0.6*nz.bp(0, 900*p, 4)*decay(t, 0.004)*softAttack(t, 1) +
				0.3*sine(200*p, t)*decay(t, 0.01)
		}},
	{ID: "marbly", Name: "Marbly", Desc: "Creamy with a touch of polish, like marbles tapping. A bit brighter.", secs: 0.04, vol: 0.34,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			return 0.8*nz.bp(0, 1300*p, 5)*decay(t, 0.005)*softAttack(t, 1) +
				0.25*nz.bp(1, 2600*p, 6)*decay(t, 0.003)*softAttack(t, 0.8) +
				0.3*sine(260*p, t)*decay(t, 0.008)
		}},
	{ID: "creamyup", Name: "Creamy, with key release", Desc: "Creamy press plus the quieter sound of the key coming back up.", secs: 0.11, vol: 0.36,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			v := creamyKey(t, nz, p, 300, 750)
			if t2 := t - 0.075; t2 >= 0 {
				v += 0.35 * (nz.bp(2, 900*p, 3)*decay(t2, 0.005)*softAttack(t2, 1.5) + 0.4*nz.bp(3, 380*p, 2)*decay(t2, 0.008))
			}
			return v
		}},
	{ID: "soft", Name: "Soft tick", Desc: "Light and quiet. The current sound.", secs: 0.032, vol: 0.30,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			return 0.55*nz.bright()*decay(t, 0.0022) + 0.30*sine(2300*p, t)*decay(t, 0.005) + 0.35*sine(190*p, t)*decay(t, 0.009)
		}},
	{ID: "clicky", Name: "Clicky mechanical", Desc: "Sharp click with a bright ping, like blue-switch keyboards.", secs: 0.05, vol: 0.42,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			t2 := t - 0.011 // the key bottoming out
			v := 0.8*nz.bright()*decay(t, 0.0012) + 0.25*sine(4200*p, t)*decay(t, 0.004) + 0.2*sine(2900*p, t)*decay(t, 0.006)
			if t2 >= 0 {
				v += 0.4*nz.bright()*decay(t2, 0.002) + 0.35*sine(240*p, t2)*decay(t2, 0.012)
			}
			return v
		}},
	{ID: "thock", Name: "Deep thock", Desc: "Low, rounded and muted, like a heavy custom keyboard.", secs: 0.06, vol: 0.55,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			return 0.7*nz.dull(0.18)*decay(t, 0.004) + 0.6*sine(130*p, t)*decay(t, 0.018) + 0.3*sine(310*p, t)*decay(t, 0.01)
		}},
	{ID: "typewriter", Name: "Typewriter", Desc: "A crisp strike with a short metallic ring.", secs: 0.09, vol: 0.42,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			ring := 0.15*sine(1850*p, t) + 0.1*sine(3120*p, t) + 0.07*sine(4470*p, t)
			return 0.9*nz.bright()*decay(t, 0.0025) + ring*decay(t, 0.03) + 0.4*sine(160*p, t)*decay(t, 0.02)
		}},
	{ID: "bubble", Name: "Bubble pop", Desc: "A soft, playful pop that drops in pitch.", secs: 0.07, vol: 0.40,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			// frequency sweeps down; integrate it for the phase
			f0, f1, tau := 1400*p, 500*p, 0.012
			phase := 2 * math.Pi * (f1*t + f0*tau*(1-math.Exp(-t/tau)))
			att := math.Min(1, t/0.002)
			return math.Sin(phase) * att * decay(t, 0.02)
		}},
	{ID: "glass", Name: "Glass tap", Desc: "A tiny, clear chime. Subtle and airy.", secs: 0.12, vol: 0.28,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			return 0.5*sine(2600*p, t)*decay(t, 0.035) + 0.2*sine(5230*p, t)*decay(t, 0.02) + 0.15*nz.bright()*decay(t, 0.001)
		}},
	{ID: "laptop", Name: "Laptop keys", Desc: "Very short and muffled, like a quiet laptop keyboard.", secs: 0.025, vol: 0.45,
		gen: func(t float64, nz *noiseGen, p float64) float64 {
			return 0.8*nz.dull(0.35)*decay(t, 0.0018) + 0.2*sine(400*p, t)*decay(t, 0.004)
		}},
}

func styleByID(id string) clickStyle {
	for _, s := range clickStyles {
		if s.ID == id {
			return s
		}
	}
	return clickStyles[0]
}

// renderClick: samples for one key press. variant changes pitch and texture slightly.
func renderClick(st clickStyle, variant int) []float64 {
	pitches := []float64{1.0, 0.95, 1.05, 0.98, 1.03}
	p := pitches[variant%len(pitches)]
	nz := &noiseGen{seed: uint32(0x9E3779B9 * (variant + 1))}
	n := int(st.secs * synthRate)
	out := make([]float64, n)
	peak := 0.0
	for i := range out {
		t := float64(i) / synthRate
		v := st.gen(t, nz, p)
		if i < 22 { // tiny fade-in so it never pops
			v *= float64(i) / 22
		}
		if tail := n - i; tail < 60 { // and a fade-out
			v *= float64(tail) / 60
		}
		out[i] = v
		peak = math.Max(peak, math.Abs(v))
	}
	if peak > 0 { // every sound equally loud: vol is its peak level
		for i := range out {
			out[i] *= st.vol / peak
		}
	}
	return out
}

func toWAV(samples []float64) []byte {
	data := make([]byte, len(samples)*2)
	for i, v := range samples {
		v = math.Max(-1, math.Min(1, v))
		binary.LittleEndian.PutUint16(data[i*2:], uint16(int16(v*32767)))
	}
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(data)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], synthRate)
	binary.LittleEndian.PutUint32(h[28:], synthRate*2)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(data)))
	return append(h, data...)
}

// ---- the editable clean click (same maths as the online sound editor) ----

// ClickParams: the settings the sound editor produces
type ClickParams struct {
	Pitch      float64 `json:"pitch"`      // semitones
	Length     float64 `json:"length"`     // decay multiplier
	Brightness float64 `json:"brightness"` // top-end sparkle, 0-2
	Body       float64 `json:"body"`       // low-mid weight, 0-2
	Attack     float64 `json:"attack"`     // ms; lower = sharper
	Ring       float64 `json:"ring"`       // resonance (Q) multiplier
	Release    bool    `json:"release"`    // key-release click
	RelLevel   float64 `json:"relLevel"`   // its level, 0-1
	RelDelay   float64 `json:"relDelay"`   // ms after the press
	Volume     float64 `json:"volume"`     // peak, 0-1
	Variation  float64 `json:"variation"`  // pitch spread between versions, 0-0.1
}

var defaultClick = ClickParams{Pitch: 3, Length: 1, Brightness: 1, Body: 1, Attack: 2.2, Ring: 1,
	Release: true, RelLevel: 0.58, RelDelay: 86, Volume: 0.32, Variation: 0.05}

func renderCustomClick(c ClickParams, variant int) []float64 {
	spreads := []float64{0, -1, 1, -0.4, 0.6}
	p := 1 + c.Variation*spreads[variant%len(spreads)]
	k := math.Pow(2, c.Pitch/12) * p
	L, q := math.Max(c.Length, 0.1), math.Max(c.Ring, 0.1)
	att := math.Max(c.Attack, 0.05)
	rd := c.RelDelay / 1000
	secs := 0.045*L + 0.005
	if c.Release {
		secs = rd + 0.045*L + 0.005
	}
	nz := &noiseGen{seed: uint32(0x9E3779B9 * (variant + 1))}
	n := int(secs * synthRate)
	out := make([]float64, n)
	peak := 0.0
	for i := range out {
		t := float64(i) / synthRate
		a := softAttack(t, att)
		v := a*(1.15*nz.bp(0, 1722*k, 8*q)*decay(t, 0.004*L)+
			0.8*nz.bp(1, 1426*k, 8*q)*decay(t, 0.004*L)+
			c.Body*(0.75*nz.bp(2, 645*k, 5*q)*decay(t, 0.005*L)+0.25*nz.bp(11, 759*k, 6*q)*decay(t, 0.005*L))+
			c.Brightness*(0.32*nz.bp(3, 2761*k, 6*q)*decay(t, 0.003*L)+0.30*nz.bp(10, 4274*k, 5*q)*decay(t, 0.0025*L)+
				1.0*nz.bp(4, 5211*k, 3*q)*decay(t, 0.002*L)+0.3*nz.bright()*decay(t, 0.0015*L))) +
			0.12*nz.bp(5, 1722*k, 15*q)*decay(t, 0.009*L)
		if t2 := t - rd; c.Release && t2 >= 0 {
			v += c.RelLevel * softAttack(t2, att) * (c.Body*(0.6*nz.bp(6, 450*k, 4*q)+0.5*nz.bp(7, 662*k, 5*q)) +
				0.6*nz.bp(8, 1448*k, 8*q) + 0.5*nz.bp(9, 1722*k, 8*q) + c.Brightness*0.25*nz.bp(12, 5211*k, 4*q)) * decay(t2, 0.004*L)
		}
		if i < 22 {
			v *= float64(i) / 22
		}
		if tail := n - i; tail < 60 {
			v *= float64(tail) / 60
		}
		out[i] = v
		peak = math.Max(peak, math.Abs(v))
	}
	if peak > 0 {
		for i := range out {
			out[i] *= c.Volume / peak
		}
	}
	return out
}
