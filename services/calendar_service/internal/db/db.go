// Package db is the typed access of calendar_service to database_middleware. Only the operations that
// calendar_service may call exist in the generated client (x-allowed-callers).
package db

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/mwclient"
)

// Uuid and the record types used by the callers.
type (
	Uuid    = openapi_types.UUID
	Booking = mwclient.BookingRecord
)

// Error is a failed middleware call; Status and Code come from its problem answer.
type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string { return http.StatusText(e.Status) + ": " + e.Code }

// Status returns the HTTP status of a middleware error, or 0.
func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// Code returns the problem code of a middleware error, or "".
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// NotFound reports a 404 of the middleware.
func NotFound(err error) bool { return Status(err) == http.StatusNotFound }

// SlotTaken reports the overlap conflict of the exclusion constraint.
func SlotTaken(err error) bool {
	return Status(err) == http.StatusConflict && Code(err) == "slot_taken"
}

func fail(status int, body []byte) error {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &p)
	return &Error{Status: status, Code: p.Code}
}

func pick[T any](v *T, status int, body []byte) (*T, error) {
	if v == nil {
		return nil, fail(status, body)
	}
	return v, nil
}

func done(status int, body []byte) error {
	if status == http.StatusNoContent {
		return nil
	}
	return fail(status, body)
}

type actKey struct{}

// WithActor records the end user on whose behalf calls are made (claim act of the service token, audit only).
func WithActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, actKey{}, userID)
}

// DB talks to database_middleware.
type DB struct{ c *mwclient.ClientWithResponses }

// New creates the client. Every request carries a fresh service token (iss=calendar_service, at most 60 s).
func New(baseURL string, signer *praxisauth.ServiceSigner) (*DB, error) {
	c, err := mwclient.NewClientWithResponses(baseURL,
		mwclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}),
		mwclient.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			act, _ := ctx.Value(actKey{}).(string)
			req.Header.Set("Authorization", "Bearer "+signer.Token("database_middleware", act))
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return &DB{c: c}, nil
}

// TokenValid asks whether a user token is still valid (revocation state).
func (d *DB) TokenValid(ctx context.Context, jti, sub Uuid, iat time.Time) (bool, error) {
	r, err := d.c.TokenCheckWithResponse(ctx, mwclient.TokenCheckParams{Jti: jti, Sub: sub, Iat: iat})
	if err != nil {
		return false, err
	}
	v, err := pick(r.JSON200, r.StatusCode(), r.Body)
	if err != nil {
		return false, err
	}
	return v.Valid, nil
}

// ---- specialties and services

func (d *DB) SpecialtyList(ctx context.Context) (*mwclient.SpecialtyList, error) {
	r, err := d.c.SpecialtyListWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) SpecialtyCreate(ctx context.Context, name string) (*mwclient.Specialty, error) {
	r, err := d.c.SpecialtyCreateWithResponse(ctx, mwclient.SpecialtyInput{Name: name})
	if err != nil {
		return nil, err
	}
	return pick(r.JSON201, r.StatusCode(), r.Body)
}

func (d *DB) SpecialtyDelete(ctx context.Context, id Uuid) error {
	r, err := d.c.SpecialtyDeleteWithResponse(ctx, mwclient.IdParam{Id: id})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) ServiceList(ctx context.Context) (*mwclient.ServiceList, error) {
	r, err := d.c.ServiceListWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) ServiceCreate(ctx context.Context, p mwclient.ServiceInput) (*mwclient.Service, error) {
	r, err := d.c.ServiceCreateWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON201, r.StatusCode(), r.Body)
}

func (d *DB) ServiceUpdate(ctx context.Context, p mwclient.ServiceUpdateParams) (*mwclient.Service, error) {
	r, err := d.c.ServiceUpdateWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) ServiceDelete(ctx context.Context, id Uuid) error {
	r, err := d.c.ServiceDeleteWithResponse(ctx, mwclient.IdParam{Id: id})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) ServiceImport(ctx context.Context, p mwclient.ServiceImportParams) (*mwclient.ImportResult, error) {
	r, err := d.c.ServiceImportWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

// ---- doctors and patients

func (d *DB) DoctorList(ctx context.Context, specialty *Uuid) (*mwclient.DoctorList, error) {
	r, err := d.c.DoctorListWithResponse(ctx, mwclient.DoctorListParams{SpecialtyId: specialty})
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) DoctorGetSchedule(ctx context.Context, id Uuid) (*mwclient.Schedule, error) {
	r, err := d.c.DoctorGetScheduleWithResponse(ctx, mwclient.DoctorIdParam{DoctorId: id})
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) DoctorSetSchedule(ctx context.Context, p mwclient.DoctorScheduleParams) (*mwclient.ScheduleResult, error) {
	r, err := d.c.DoctorSetScheduleWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) PatientList(ctx context.Context, p mwclient.PatientListParams) (*mwclient.PatientPage, error) {
	r, err := d.c.PatientListWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) PatientGet(ctx context.Context, id Uuid) (*mwclient.Patient, error) {
	r, err := d.c.PatientGetWithResponse(ctx, mwclient.PatientIdParam{PatientId: id})
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) PatientSetMainDoctor(ctx context.Context, patient Uuid, doctor *Uuid) (*mwclient.Patient, error) {
	r, err := d.c.PatientSetMainDoctorWithResponse(ctx, mwclient.MainDoctorParams{PatientId: patient, DoctorId: doctor})
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

// ---- calendar and bookings

func (d *DB) CalendarGetWindows(ctx context.Context, p mwclient.WindowsParams) (*mwclient.WindowsResult, error) {
	r, err := d.c.CalendarGetWindowsWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) BookingCreate(ctx context.Context, p mwclient.BookingInsert) (*Booking, error) {
	r, err := d.c.BookingCreateWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON201, r.StatusCode(), r.Body)
}

func (d *DB) BookingList(ctx context.Context, p mwclient.BookingListParams) (*mwclient.BookingPage, error) {
	r, err := d.c.BookingListWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) BookingGet(ctx context.Context, p mwclient.BookingGetParams) (*Booking, error) {
	r, err := d.c.BookingGetWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) BookingUpdate(ctx context.Context, p mwclient.BookingUpdateParams) (*Booking, error) {
	r, err := d.c.BookingUpdateWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) BookingCancel(ctx context.Context, p mwclient.BookingGetParams) (*Booking, error) {
	r, err := d.c.BookingCancelWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) BookingAssignDoctor(ctx context.Context, booking, doctor Uuid) (*Booking, error) {
	r, err := d.c.BookingAssignDoctorWithResponse(ctx, mwclient.BookingAssignParams{BookingId: booking, DoctorId: doctor})
	if err != nil {
		return nil, err
	}
	return pick(r.JSON200, r.StatusCode(), r.Body)
}

func (d *DB) AuditWrite(ctx context.Context, p mwclient.AuditWriteParams) error {
	r, err := d.c.AuditWriteWithResponse(ctx, p)
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}
