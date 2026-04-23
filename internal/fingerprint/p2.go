package fingerprint

import (
	"encoding/json"
	"math"
	"sort"
)

// P2Quantile implements the P-Square algorithm for streaming quantile estimation.
// It maintains 5 markers and computes quantile estimates in O(1) per update.
type P2Quantile struct {
	p       float64    // target quantile (0-1)
	q       [5]float64 // marker heights (quantile estimates)
	n       [5]int     // marker positions
	nPrime  [5]float64 // desired marker positions
	dn      [5]float64 // desired position increments
	count   int        // total observations
	initial []float64  // buffer for first 5 observations
}

// NewP2Quantile creates a new P2 streaming quantile estimator for the given quantile p.
func NewP2Quantile(p float64) *P2Quantile {
	return &P2Quantile{
		p:       p,
		initial: make([]float64, 0, 5),
		dn:      [5]float64{0, p / 2, p, (1 + p) / 2, 1},
	}
}

// Add adds an observation to the estimator.
func (pq *P2Quantile) Add(x float64) {
	pq.count++

	// Initialization phase: collect first 5 observations
	if pq.count <= 5 {
		pq.initial = append(pq.initial, x)
		if pq.count == 5 {
			pq.initialize()
		}
		return
	}

	// Find cell k where x falls
	var k int
	switch {
	case x < pq.q[0]:
		pq.q[0] = x
		k = 0
	case x < pq.q[1]:
		k = 0
	case x < pq.q[2]:
		k = 1
	case x < pq.q[3]:
		k = 2
	case x <= pq.q[4]:
		k = 3
	default:
		pq.q[4] = x
		k = 3
	}

	// Increment positions of markers k+1 through 4
	for i := k + 1; i < 5; i++ {
		pq.n[i]++
	}

	// Update desired positions
	for i := 0; i < 5; i++ {
		pq.nPrime[i] += pq.dn[i]
	}

	// Adjust marker heights using P² formula
	for i := 1; i < 4; i++ {
		d := pq.nPrime[i] - float64(pq.n[i])
		if (d >= 1 && pq.n[i+1]-pq.n[i] > 1) || (d <= -1 && pq.n[i-1]-pq.n[i] < -1) {
			sign := 1
			if d < 0 {
				sign = -1
			}
			// Try parabolic (P²) formula
			qi := pq.parabolic(i, float64(sign))
			if qi > pq.q[i-1] && qi < pq.q[i+1] {
				pq.q[i] = qi
			} else {
				// Fall back to linear
				pq.q[i] = pq.linear(i, sign)
			}
			pq.n[i] += sign
		}
	}
}

// Estimate returns the current quantile estimate.
func (pq *P2Quantile) Estimate() float64 {
	if pq.count < 5 {
		if pq.count == 0 {
			return 0
		}
		// Not enough data; sort and return nearest rank
		sorted := make([]float64, len(pq.initial))
		copy(sorted, pq.initial)
		sort.Float64s(sorted)
		idx := int(math.Round(pq.p * float64(len(sorted)-1)))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	return pq.q[2]
}

// Count returns the number of observations.
func (pq *P2Quantile) Count() int {
	return pq.count
}

func (pq *P2Quantile) initialize() {
	sort.Float64s(pq.initial)
	for i := 0; i < 5; i++ {
		pq.q[i] = pq.initial[i]
		pq.n[i] = i
		pq.nPrime[i] = float64(i)
	}
	pq.initial = nil // free buffer
}

func (pq *P2Quantile) parabolic(i int, d float64) float64 {
	qi := pq.q[i]
	qPrev := pq.q[i-1]
	qNext := pq.q[i+1]
	ni := float64(pq.n[i])
	nPrev := float64(pq.n[i-1])
	nNext := float64(pq.n[i+1])

	a := d / (nNext - nPrev)
	b := (ni - nPrev + d) * (qNext - qi) / (nNext - ni)
	c := (nNext - ni - d) * (qi - qPrev) / (ni - nPrev)

	return qi + a*(b+c)
}

func (pq *P2Quantile) linear(i, sign int) float64 {
	qi := pq.q[i]
	j := i + sign
	return qi + float64(sign)*(pq.q[j]-qi)/float64(pq.n[j]-pq.n[i])
}

// p2JSON is the serialization format for P2Quantile.
type p2JSON struct {
	P       float64    `json:"p"`
	Q       [5]float64 `json:"q"`
	N       [5]int     `json:"n"`
	NPrime  [5]float64 `json:"n_prime"`
	DN      [5]float64 `json:"dn"`
	Count   int        `json:"count"`
	Initial []float64  `json:"initial,omitempty"`
}

// MarshalJSON implements json.Marshaler.
func (pq *P2Quantile) MarshalJSON() ([]byte, error) {
	return json.Marshal(p2JSON{
		P:       pq.p,
		Q:       pq.q,
		N:       pq.n,
		NPrime:  pq.nPrime,
		DN:      pq.dn,
		Count:   pq.count,
		Initial: pq.initial,
	})
}

// UnmarshalJSON implements json.Unmarshaler.
func (pq *P2Quantile) UnmarshalJSON(data []byte) error {
	var v p2JSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	pq.p = v.P
	pq.q = v.Q
	pq.n = v.N
	pq.nPrime = v.NPrime
	pq.dn = v.DN
	pq.count = v.Count
	pq.initial = v.Initial
	return nil
}
