// Package erp is the fixture-backed ERP the accounts-payable demo
// investigates. It is a SYSTEM OF RECORD, not a control: it
// answers what it is asked, holds no credential, reaches nothing, and
// refuses nothing an ordinary ERP would accept. Every guarantee the demo
// makes is made by the governance plane in front of it.
//
// The corpus lives in a ConfigMap, never in this binary, so the story can
// be edited without a rebuild. What the binary DOES own is the arithmetic:
// the demo's credibility is that an audience can check the numbers, and a
// ConfigMap is exactly where a silent inconsistency would appear. Load
// refuses a corpus that does not add up — loudly, at boot, the way the
// plane refuses a malformed policy table.
package erp

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Vendor is a party invoices come from and payments go to. payee_id is
// separate from vendor_id on purpose: substituting the payee is the AP
// fraud pattern the injection case demonstrates.
type Vendor struct {
	VendorID string `json:"vendor_id"`
	Name     string `json:"name"`
	PayeeID  string `json:"payee_id"`
}

// PurchaseOrder is what was ordered and at what price.
type PurchaseOrder struct {
	POID           string `json:"po_id"`
	VendorID       string `json:"vendor_id"`
	Description    string `json:"description"`
	Quantity       int64  `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	TotalCents     int64  `json:"total_cents"`
	// AuthorizedFees names every fee the buyer authorized in writing.
	// Empty is the interesting case: an invoice that carries one anyway.
	AuthorizedFees []string `json:"authorized_fees"`
}

// ReceivingRecord is what the dock actually took in.
type ReceivingRecord struct {
	RecordID     string `json:"record_id"`
	POID         string `json:"po_id"`
	Received     int64  `json:"received"`
	Backordered  int64  `json:"backordered"`
	ReceivedDate string `json:"received_date"`
}

// Contract is the terms the vendor agreed to.
type Contract struct {
	ContractID string   `json:"contract_id"`
	VendorID   string   `json:"vendor_id"`
	Terms      []string `json:"terms"`
}

// InvoiceLine is one charge. quantity x unit_price_cents must equal
// amount_cents; the lines must sum to the invoice total.
type InvoiceLine struct {
	Description    string `json:"description"`
	Quantity       int64  `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	AmountCents    int64  `json:"amount_cents"`
	// Fee marks a line that is not goods — the kind of charge a contract
	// has to authorize.
	Fee bool `json:"fee"`
}

// Invoice is what the vendor is asking to be paid.
type Invoice struct {
	InvoiceID  string        `json:"invoice_id"`
	VendorID   string        `json:"vendor_id"`
	POID       string        `json:"po_id"`
	Status     string        `json:"status"`
	IssuedDate string        `json:"issued_date"`
	TotalCents int64         `json:"total_cents"`
	Lines      []InvoiceLine `json:"lines"`
	// Notes is free text that arrived WITH the invoice — vendor-supplied,
	// untrusted, and reproduced verbatim. The injection case lives here.
	Notes string `json:"notes"`
}

// PaymentPolicy is the buyer's own rulebook, which the agent reads.
type PaymentPolicy struct {
	Rules []string `json:"rules"`
	// ApprovalThresholdCents states in the corpus what the plane enforces
	// in configuration. They are two different things and the demo says so:
	// this is a sentence an agent can read, the standing constraint in
	// k8s/plane/upstreams.yaml is the rule that actually binds.
	ApprovalThresholdCents int64 `json:"approval_threshold_cents"`
}

// Fixtures is the whole corpus.
type Fixtures struct {
	Vendors        map[string]Vendor            `json:"vendors"`
	PurchaseOrders map[string]PurchaseOrder     `json:"purchase_orders"`
	Receiving      map[string][]ReceivingRecord `json:"receiving"`
	Contracts      map[string]Contract          `json:"contracts"`
	Invoices       map[string]Invoice           `json:"invoices"`
	PaymentPolicy  PaymentPolicy                `json:"payment_policy"`
}

// Load reads and validates a corpus. Every failure is fatal at boot: a
// half-consistent ERP would make the demo's arithmetic unfalsifiable.
func Load(r io.Reader) (*Fixtures, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var f Fixtures
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("fixtures: %w", err)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

func (f *Fixtures) validate() error {
	if len(f.Vendors) == 0 || len(f.Invoices) == 0 {
		return fmt.Errorf("fixtures: a corpus needs at least one vendor and one invoice")
	}
	for id, v := range f.Vendors {
		if v.VendorID != id {
			return fmt.Errorf("fixtures: vendor %q is keyed as %q", v.VendorID, id)
		}
		if v.Name == "" || v.PayeeID == "" {
			return fmt.Errorf("fixtures: vendor %q needs a name and a payee_id", id)
		}
	}
	for id, po := range f.PurchaseOrders {
		if po.POID != id {
			return fmt.Errorf("fixtures: purchase order %q is keyed as %q", po.POID, id)
		}
		if _, ok := f.Vendors[po.VendorID]; !ok {
			return fmt.Errorf("fixtures: purchase order %q names unknown vendor %q", id, po.VendorID)
		}
		if got := po.Quantity * po.UnitPriceCents; got != po.TotalCents {
			return fmt.Errorf("fixtures: purchase order %q: %d x %d = %d, but total_cents is %d",
				id, po.Quantity, po.UnitPriceCents, got, po.TotalCents)
		}
	}
	for poID, records := range f.Receiving {
		po, ok := f.PurchaseOrders[poID]
		if !ok {
			return fmt.Errorf("fixtures: receiving names unknown purchase order %q", poID)
		}
		var received, backordered int64
		for _, rec := range records {
			if rec.POID != poID {
				return fmt.Errorf("fixtures: receiving record %q is filed under %q", rec.RecordID, poID)
			}
			received += rec.Received
			backordered += rec.Backordered
		}
		if received+backordered != po.Quantity {
			return fmt.Errorf("fixtures: purchase order %q ordered %d, but receiving accounts for %d received + %d backordered",
				poID, po.Quantity, received, backordered)
		}
	}
	for id, c := range f.Contracts {
		if c.VendorID != id {
			return fmt.Errorf("fixtures: contract for %q is keyed as %q", c.VendorID, id)
		}
		if _, ok := f.Vendors[id]; !ok {
			return fmt.Errorf("fixtures: contract names unknown vendor %q", id)
		}
	}
	for id, inv := range f.Invoices {
		if inv.InvoiceID != id {
			return fmt.Errorf("fixtures: invoice %q is keyed as %q", inv.InvoiceID, id)
		}
		if _, ok := f.Vendors[inv.VendorID]; !ok {
			return fmt.Errorf("fixtures: invoice %q names unknown vendor %q", id, inv.VendorID)
		}
		if inv.POID != "" {
			po, ok := f.PurchaseOrders[inv.POID]
			if !ok {
				return fmt.Errorf("fixtures: invoice %q names unknown purchase order %q", id, inv.POID)
			}
			if po.VendorID != inv.VendorID {
				return fmt.Errorf("fixtures: invoice %q is from %q but purchase order %q is %q's",
					id, inv.VendorID, inv.POID, po.VendorID)
			}
		}
		if len(inv.Lines) == 0 {
			return fmt.Errorf("fixtures: invoice %q has no lines", id)
		}
		var sum int64
		for i, l := range inv.Lines {
			if got := l.Quantity * l.UnitPriceCents; got != l.AmountCents {
				return fmt.Errorf("fixtures: invoice %q line %d: %d x %d = %d, but amount_cents is %d",
					id, i+1, l.Quantity, l.UnitPriceCents, got, l.AmountCents)
			}
			sum += l.AmountCents
		}
		if sum != inv.TotalCents {
			return fmt.Errorf("fixtures: invoice %q: lines sum to %d, but total_cents is %d", id, sum, inv.TotalCents)
		}
	}
	return nil
}

// InvoicesFor lists a vendor's invoices, or all of them when vendorID is
// empty, in invoice-id order so a listing is stable between calls.
func (f *Fixtures) InvoicesFor(vendorID string) []Invoice {
	out := []Invoice{}
	for _, inv := range f.Invoices {
		if vendorID == "" || inv.VendorID == vendorID {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InvoiceID < out[j].InvoiceID })
	return out
}
