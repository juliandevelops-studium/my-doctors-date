package server

import (
	"context"
	"net/http"
	"sort"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/mailer"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/mwclient"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/slots"
)

// bookingOf maps a middleware record to the API. The patient is only included for doctors and staff: a patient
// sees the own booking without a patient block.
func bookingOf(b *db.Booking, withPatient bool) api.Booking {
	out := api.Booking{Id: b.Id, StartsAt: b.StartsAt, EndsAt: b.EndsAt, Status: api.BookingStatus(b.Status), Reason: b.Reason, Info: b.Info}
	out.Service.Id, out.Service.Name = b.ServiceId, b.ServiceName
	if b.DoctorId != nil && b.DoctorFirstName != nil && b.DoctorLastName != nil {
		out.Doctor = &api.PersonRef{Id: *b.DoctorId, FirstName: *b.DoctorFirstName, LastName: *b.DoctorLastName}
	}
	if withPatient && b.PatientId != nil && b.PatientFirstName != nil && b.PatientLastName != nil {
		p := &api.PatientRef{Id: *b.PatientId, FirstName: *b.PatientFirstName, LastName: *b.PatientLastName, Phone: b.PatientPhone}
		if b.PatientEmail != nil {
			e := openapi_types.Email(*b.PatientEmail)
			p.Email = &e
		}
		out.Patient = p
	}
	return out
}

// mailOf builds the booking mail if the patient has an e-mail address.
func mailOf(b *db.Booking) (mailer.BookingMail, bool) {
	if b.PatientEmail == nil || b.PatientFirstName == nil {
		return mailer.BookingMail{}, false
	}
	m := mailer.BookingMail{To: string(*b.PatientEmail), FirstName: *b.PatientFirstName, Service: b.ServiceName, StartsAt: b.StartsAt}
	if b.DoctorFirstName != nil && b.DoctorLastName != nil {
		m.Doctor = *b.DoctorFirstName + " " + *b.DoctorLastName
	}
	return m, true
}

// withPatientInfo is true for everyone except a caller who is only a patient.
func withPatientInfo(id Identity) bool { return id.staff() || id.has("doctor") }

// owners are the ownership filters under which the caller may see a booking (SR.03). The database applies them, so a
// foreign booking is simply not found. Staff see everything; a doctor sees the own calendar; a patient the own
// bookings. A person with several roles may see the union, so these are alternatives.
type owner struct{ patient, doctor *db.Uuid }

func (s *Server) owners(id Identity) []owner {
	if id.staff() {
		return []owner{{}}
	}
	var out []owner
	u := id.UserID
	if id.has("patient") {
		out = append(out, owner{patient: &u})
	}
	if id.has("doctor") {
		out = append(out, owner{doctor: &u})
	}
	return out
}

// find loads a booking the caller may see, trying each ownership alternative.
func (s *Server) find(ctx context.Context, id Identity, booking db.Uuid) (*db.Booking, owner, error) {
	for _, o := range s.owners(id) {
		b, err := s.DB.BookingGet(ctx, mwclient.BookingGetParams{BookingId: booking, OwnerPatientId: o.patient, OwnerDoctorId: o.doctor})
		if err == nil {
			return b, o, nil
		}
		if !db.NotFound(err) {
			return nil, owner{}, mapErr(err)
		}
	}
	return nil, owner{}, notFound()
}

// ListBookings lists the bookings in the scope of the caller. Filters by patient, doctor and "unassigned" are only
// honoured for staff.
func (s *Server) ListBookings(ctx context.Context, req api.ListBookingsRequestObject) (api.ListBookingsResponseObject, error) {
	id := identityFrom(ctx)
	p := req.Params
	base := mwclient.BookingListParams{From: p.From, To: p.To, Limit: p.Limit, Offset: p.Offset}
	if p.When != nil {
		w := mwclient.BookingListParamsWhen(*p.When)
		base.When = &w
	}
	if id.staff() {
		base.PatientId, base.DoctorId, base.Unassigned = p.PatientId, p.DoctorId, p.Unassigned
	}
	owners := s.owners(id)
	var page *mwclient.BookingPage
	switch len(owners) {
	case 0:
		return nil, forbidden()
	case 1:
		base.OwnerPatientId, base.OwnerDoctorId = owners[0].patient, owners[0].doctor
		var err error
		if page, err = s.DB.BookingList(ctx, base); err != nil {
			return nil, mapErr(err)
		}
	default: // a doctor who is also a patient: own calendar plus own bookings, merged
		limit, offset := 50, 0
		if p.Limit != nil {
			limit = *p.Limit
		}
		if p.Offset != nil {
			offset = *p.Offset
		}
		merged, err := s.listMerged(ctx, base, owners, limit, offset)
		if err != nil {
			return nil, err
		}
		page = merged
	}
	out := api.BookingPage{Items: make([]api.Booking, len(page.Items)), Total: page.Total}
	for i := range page.Items {
		out.Items[i] = bookingOf(&page.Items[i], withPatientInfo(id))
	}
	return api.ListBookings200JSONResponse(out), nil
}

// listMerged fetches the pages of several ownership filters and merges them: a doctor who is also a patient sees
// the union, and a booking that appears in both lists counts once.
func (s *Server) listMerged(ctx context.Context, base mwclient.BookingListParams, owners []owner, limit, offset int) (*mwclient.BookingPage, error) {
	window := min(offset+limit, 200)
	merged := map[db.Uuid]db.Booking{}
	total, fetched := 0, 0
	for _, o := range owners {
		q := base
		q.OwnerPatientId, q.OwnerDoctorId, q.Offset, q.Limit = o.patient, o.doctor, nil, &window
		part, err := s.DB.BookingList(ctx, q)
		if err != nil {
			return nil, mapErr(err)
		}
		total += part.Total
		fetched += len(part.Items)
		for _, b := range part.Items {
			merged[b.Id] = b
		}
	}
	items := make([]db.Booking, 0, len(merged))
	for _, b := range merged {
		items = append(items, b)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].StartsAt.Before(items[j].StartsAt) })
	if offset > len(items) {
		offset = len(items)
	}
	items = items[offset:min(offset+limit, len(items))]
	return &mwclient.BookingPage{Items: items, Total: total - (fetched - len(merged))}, nil
}

// GetBooking shows one booking including reason and infos. A booking the caller may not see does not exist (404).
func (s *Server) GetBooking(ctx context.Context, req api.GetBookingRequestObject) (api.GetBookingResponseObject, error) {
	id := identityFrom(ctx)
	b, _, err := s.find(ctx, id, req.BookingId)
	if err != nil {
		return nil, err
	}
	return api.GetBooking200JSONResponse(bookingOf(b, withPatientInfo(id))), nil
}

// CancelBooking cancels a future booking (UF-07, UF-08). No cancellation deadline (decision 6).
func (s *Server) CancelBooking(ctx context.Context, req api.CancelBookingRequestObject) (api.CancelBookingResponseObject, error) {
	id := identityFrom(ctx)
	_, o, err := s.find(ctx, id, req.BookingId)
	if err != nil {
		return nil, err
	}
	b, err := s.DB.BookingCancel(ctx, mwclient.BookingGetParams{BookingId: req.BookingId, OwnerPatientId: o.patient, OwnerDoctorId: o.doctor})
	if err != nil {
		return nil, mapErr(err)
	}
	if m, ok := mailOf(b); ok {
		s.Mail.Cancelled(ctx, m)
	}
	return api.CancelBooking204Response{}, nil
}

// candidates computes the doctors who could take a booking and their calendar data around the start.
type candidate struct {
	id      db.Uuid
	working []slots.Interval
	busy    []slots.Interval
	load    time.Duration
}

// aroundSlot widens a slot by 12 hours on each side: the window the middleware is asked for around a booking.
func aroundSlot(slot slots.Interval) slots.Interval {
	return slots.Interval{Start: slot.Start.Add(-12 * time.Hour), End: slot.End.Add(12 * time.Hour)}
}

// checkStartTime validates a requested start: whole minutes, and in the future.
func checkStartTime(start, now time.Time) *apiError {
	if start.Second() != 0 || start.Nanosecond() != 0 {
		return validation("starts_at", "whole_minutes")
	}
	if !start.After(now) {
		return unprocessable("starts_in_past", "The appointment must be in the future")
	}
	return nil
}

// slotForService is the interval a service occupies when it starts at start.
func slotForService(svc *mwclient.Service, start time.Time) slots.Interval {
	return slots.Interval{Start: start, End: start.Add(time.Duration(svc.DurationMinutes) * time.Minute)}
}

// checkDoctorSlot verifies that the doctor works at the slot and offers the service (a normal service via its
// specialty). When updating, the own booking is excluded from "busy" by the database, so only working time matters.
func (s *Server) checkDoctorSlot(ctx context.Context, doctorID, specialty *db.Uuid, slot slots.Interval) error {
	around := aroundSlot(slot)
	res, err := s.DB.CalendarGetWindows(ctx, mwclient.WindowsParams{From: around.Start, To: around.End, DoctorId: doctorID, SpecialtyId: specialty})
	if err != nil {
		return mapErr(err)
	}
	if len(res.Doctors) == 0 {
		return unprocessable("doctor_not_offering", "The doctor does not exist or does not offer this service")
	}
	if !slots.Contains(toSlots(res.Doctors[0].Working), slot) {
		return conflict("outside_working_time", "The doctor does not work at this time")
	}
	return nil
}

// pickDoctors returns the doctors who are free for the slot, least busy that day first. doctor and specialty restrict
// the search; both nil means every doctor.
func (s *Server) pickDoctors(ctx context.Context, slot slots.Interval, doctor, specialty *db.Uuid) ([]candidate, error) {
	around := aroundSlot(slot)
	res, err := s.DB.CalendarGetWindows(ctx, mwclient.WindowsParams{From: around.Start, To: around.End, DoctorId: doctor, SpecialtyId: specialty})
	if err != nil {
		return nil, mapErr(err)
	}
	var out []candidate
	for _, d := range res.Doctors {
		c := candidate{id: d.DoctorId, working: toSlots(d.Working), busy: toSlots(d.Busy)}
		if !slots.Contains(c.working, slot) || !slots.Free(c.busy, slot) {
			continue
		}
		c.load = slots.BusyWithin(c.busy, around)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].load != out[j].load {
			return out[i].load < out[j].load
		}
		return out[i].id.String() < out[j].id.String()
	})
	return out, nil
}

// CreateBooking books an appointment (UF-05, UF-06, UF-08).
//
// The patient is the caller; staff pass patient_id. The doctor is the main doctor of the patient, otherwise the
// least busy doctor with the matching specialty who is free ("automatische Zuordnung"). "Sonstiges" gets no doctor
// until reception assigns one. The overlap is decided by the database: if two requests race for one slot, exactly one
// wins and the other gets 409 slot_taken (it then tries the next doctor, if any).
func (s *Server) CreateBooking(ctx context.Context, req api.CreateBookingRequestObject) (api.CreateBookingResponseObject, error) {
	id := identityFrom(ctx)
	b := req.Body
	if ok, retry := s.bookings.Allow(id.UserID.String()); !ok {
		return nil, tooMany(retry)
	}
	patientID := s.patientFor(id, b.PatientId)
	if patientID == nil {
		return nil, validation("patient_id", "required")
	}
	patient, err := s.DB.PatientGet(ctx, *patientID)
	if err != nil {
		return nil, mapErr(err)
	}
	svc, err := s.service(ctx, b.ServiceId)
	if err != nil {
		return nil, err
	}
	start := b.StartsAt.UTC()
	if e := checkStartTime(start, s.Now()); e != nil {
		return nil, e
	}
	slot := slotForService(svc, start)

	create := mwclient.BookingInsert{PatientId: patient.Id, ServiceId: svc.Id, StartsAt: slot.Start, EndsAt: slot.End, Reason: b.Reason, Info: b.Info}

	explicit, restrict, specialty := doctorChoice(id, b, patient, svc)
	var created *db.Booking
	if svc.IsOther && explicit == nil {
		// "Sonstiges" gets no doctor until reception assigns one (UF-06)
		created, err = s.DB.BookingCreate(ctx, create)
		if err != nil {
			return nil, mapErr(err)
		}
	} else {
		cands, err := s.pickDoctors(ctx, slot, restrict, specialty)
		if err != nil {
			return nil, err
		}
		if len(cands) == 0 {
			if explicit != nil {
				return nil, conflict("doctor_unavailable", "The doctor is not available at this time or does not offer the service")
			}
			return nil, conflict("slot_taken", "No doctor is available at this time")
		}
		if created, err = s.createWithDoctors(ctx, create, cands); err != nil {
			return nil, err
		}
	}
	if m, ok := mailOf(created); ok {
		s.Mail.Confirmed(ctx, m)
	}
	return api.CreateBooking201JSONResponse{Body: bookingOf(created, withPatientInfo(id) && id.staff()),
		Headers: api.CreateBooking201ResponseHeaders{Location: "/v1/bookings/" + created.Id.String()}}, nil
}

// doctorChoice decides which doctor a new booking is restricted to: only staff may pass one explicitly, otherwise
// the patient's main doctor; a normal service additionally restricts the search to its specialty.
func doctorChoice(id Identity, b *api.BookingCreate, patient *mwclient.Patient, svc *mwclient.Service) (explicit, restrict, specialty *db.Uuid) {
	if id.staff() {
		explicit = b.DoctorId
	}
	restrict = explicit
	if restrict == nil {
		restrict = patient.MainDoctorId
	}
	if !svc.IsOther {
		specialty = svc.SpecialtyId
	}
	return
}

// createWithDoctors tries the candidate doctors in order. The database exclusion constraint decides the race: the
// loser gets 409 slot_taken and the next doctor is tried.
func (s *Server) createWithDoctors(ctx context.Context, create mwclient.BookingInsert, cands []candidate) (*db.Booking, error) {
	for _, c := range cands {
		d := c.id
		create.DoctorId = &d
		b, err := s.DB.BookingCreate(ctx, create)
		if err == nil {
			return b, nil
		}
		if !db.SlotTaken(err) {
			return nil, mapErr(err)
		}
	}
	return nil, conflict("slot_taken", "The time was just taken")
}

// UpdateBooking changes service, time, reason or info of a future booking (UF-06, UF-08; staff only).
func (s *Server) UpdateBooking(ctx context.Context, req api.UpdateBookingRequestObject) (api.UpdateBookingResponseObject, error) {
	b := req.Body
	cur, err := s.DB.BookingGet(ctx, mwclient.BookingGetParams{BookingId: req.BookingId})
	if err != nil {
		return nil, mapErr(err)
	}
	p := mwclient.BookingUpdateParams{BookingId: req.BookingId, Reason: b.Reason, Info: b.Info}
	moved := b.StartsAt != nil || b.ServiceId != nil
	if moved {
		svcID := cur.ServiceId
		if b.ServiceId != nil {
			svcID = *b.ServiceId
		}
		svc, err := s.service(ctx, svcID)
		if err != nil {
			return nil, err
		}
		start := cur.StartsAt
		if b.StartsAt != nil {
			start = b.StartsAt.UTC()
		}
		if e := checkStartTime(start, s.Now()); e != nil {
			return nil, e
		}
		slot := slotForService(svc, start)
		if cur.DoctorId != nil {
			var spec *db.Uuid
			if !svc.IsOther {
				spec = svc.SpecialtyId
			}
			if err := s.checkDoctorSlot(ctx, cur.DoctorId, spec, slot); err != nil {
				return nil, err
			}
		}
		sid := svc.Id
		p.ServiceId, p.StartsAt, p.EndsAt = &sid, &slot.Start, &slot.End
	}
	upd, err := s.DB.BookingUpdate(ctx, p)
	if err != nil {
		return nil, mapErr(err)
	}
	if moved {
		if m, ok := mailOf(upd); ok {
			s.Mail.Changed(ctx, m)
		}
	}
	return api.UpdateBooking200JSONResponse(bookingOf(upd, true)), nil
}

// AssignBookingDoctor assigns a doctor, e.g. to a "Sonstiges" booking (UF-06). The doctor must work at that time and,
// for a normal service, offer it. A busy doctor gives 409 slot_taken.
func (s *Server) AssignBookingDoctor(ctx context.Context, req api.AssignBookingDoctorRequestObject) (api.AssignBookingDoctorResponseObject, error) {
	cur, err := s.DB.BookingGet(ctx, mwclient.BookingGetParams{BookingId: req.BookingId})
	if err != nil {
		return nil, mapErr(err)
	}
	svc, err := s.service(ctx, cur.ServiceId)
	if err != nil {
		return nil, err
	}
	var spec *db.Uuid
	if !svc.IsOther {
		spec = svc.SpecialtyId
	}
	slot := slots.Interval{Start: cur.StartsAt, End: cur.EndsAt}
	if err := s.checkDoctorSlot(ctx, &req.Body.DoctorId, spec, slot); err != nil {
		return nil, err
	}
	upd, err := s.DB.BookingAssignDoctor(ctx, req.BookingId, req.Body.DoctorId)
	if err != nil {
		if db.Status(err) == http.StatusUnprocessableEntity {
			return nil, unprocessable("doctor_not_offering", "The doctor does not exist or does not offer this service")
		}
		return nil, mapErr(err)
	}
	if m, ok := mailOf(upd); ok {
		s.Mail.Changed(ctx, m)
	}
	return api.AssignBookingDoctor200JSONResponse(bookingOf(upd, true)), nil
}
