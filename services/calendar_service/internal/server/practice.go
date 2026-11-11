package server

import (
	"context"
	"net/http"

	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/mwclient"
)

// audit records an organiser change. Failures are logged; the change itself has already happened. It never
// carries appointment contents.
func (s *Server) audit(ctx context.Context, action string, target *db.Uuid) {
	p := mwclient.AuditWriteParams{Action: action, TargetUserId: target}
	id := identityFrom(ctx)
	a := id.UserID
	p.ActorUserId = &a
	if err := s.DB.AuditWrite(ctx, p); err != nil {
		s.Log.Error("audit write failed", "action", action, "error", err.Error())
	}
}

// ---- specialties

func (s *Server) ListSpecialties(ctx context.Context, _ api.ListSpecialtiesRequestObject) (api.ListSpecialtiesResponseObject, error) {
	l, err := s.DB.SpecialtyList(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	out := api.SpecialtyList{Items: make([]api.Specialty, len(l.Items))}
	for i, sp := range l.Items {
		out.Items[i] = api.Specialty{Id: sp.Id, Name: sp.Name}
	}
	return api.ListSpecialties200JSONResponse(out), nil
}

func (s *Server) CreateSpecialty(ctx context.Context, req api.CreateSpecialtyRequestObject) (api.CreateSpecialtyResponseObject, error) {
	sp, err := s.DB.SpecialtyCreate(ctx, req.Body.Name)
	if err != nil {
		return nil, mapErr(err)
	}
	s.audit(ctx, "specialty_created", nil)
	return api.CreateSpecialty201JSONResponse{Body: api.Specialty{Id: sp.Id, Name: sp.Name},
		Headers: api.CreateSpecialty201ResponseHeaders{Location: "/v1/specialties/" + sp.Id.String()}}, nil
}

func (s *Server) DeleteSpecialty(ctx context.Context, req api.DeleteSpecialtyRequestObject) (api.DeleteSpecialtyResponseObject, error) {
	if err := s.DB.SpecialtyDelete(ctx, req.SpecialtyId); err != nil {
		return nil, mapErr(err)
	}
	s.audit(ctx, "specialty_deleted", nil)
	return api.DeleteSpecialty204Response{}, nil
}

// ---- services (Leistungsangebot)

func serviceOf(sv mwclient.Service) api.Service {
	return api.Service{Id: sv.Id, Name: sv.Name, DurationMinutes: sv.DurationMinutes, IsOther: sv.IsOther, SpecialtyId: sv.SpecialtyId, SpecialtyName: sv.SpecialtyName}
}

func (s *Server) ListServices(ctx context.Context, _ api.ListServicesRequestObject) (api.ListServicesResponseObject, error) {
	l, err := s.DB.ServiceList(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	out := api.ServiceList{Items: make([]api.Service, len(l.Items))}
	for i, sv := range l.Items {
		out.Items[i] = serviceOf(sv)
	}
	return api.ListServices200JSONResponse(out), nil
}

func (s *Server) CreateService(ctx context.Context, req api.CreateServiceRequestObject) (api.CreateServiceResponseObject, error) {
	b := req.Body
	sv, err := s.DB.ServiceCreate(ctx, mwclient.ServiceInput{Name: b.Name, DurationMinutes: b.DurationMinutes, SpecialtyId: b.SpecialtyId})
	if err != nil {
		return nil, mapErr(err)
	}
	s.audit(ctx, "service_created", nil)
	return api.CreateService201JSONResponse{Body: serviceOf(*sv), Headers: api.CreateService201ResponseHeaders{Location: "/v1/services/" + sv.Id.String()}}, nil
}

func (s *Server) UpdateService(ctx context.Context, req api.UpdateServiceRequestObject) (api.UpdateServiceResponseObject, error) {
	b := req.Body
	sv, err := s.DB.ServiceUpdate(ctx, mwclient.ServiceUpdateParams{Id: req.ServiceId, Name: b.Name, DurationMinutes: b.DurationMinutes, SpecialtyId: b.SpecialtyId})
	if err != nil {
		return nil, mapErr(err)
	}
	s.audit(ctx, "service_updated", nil)
	return api.UpdateService200JSONResponse(serviceOf(*sv)), nil
}

func (s *Server) DeleteService(ctx context.Context, req api.DeleteServiceRequestObject) (api.DeleteServiceResponseObject, error) {
	if err := s.DB.ServiceDelete(ctx, req.ServiceId); err != nil {
		return nil, mapErr(err)
	}
	s.audit(ctx, "service_deleted", nil)
	return api.DeleteService204Response{}, nil
}

func (s *Server) ImportServices(ctx context.Context, req api.ImportServicesRequestObject) (api.ImportServicesResponseObject, error) {
	dry := req.Params.DryRun != nil && *req.Params.DryRun
	p := mwclient.ServiceImportParams{DryRun: dry}
	if req.Body.Specialties != nil {
		list := make([]struct {
			Name string `json:"name"`
		}, len(*req.Body.Specialties))
		for i, sp := range *req.Body.Specialties {
			list[i].Name = sp.Name
		}
		p.Specialties = &list
	}
	if req.Body.Services != nil {
		list := make([]struct {
			DurationMinutes int     `json:"duration_minutes"`
			IsOther         *bool   `json:"is_other,omitempty"`
			Name            string  `json:"name"`
			Specialty       *string `json:"specialty"`
		}, len(*req.Body.Services))
		for i, sv := range *req.Body.Services {
			list[i].DurationMinutes, list[i].IsOther, list[i].Name, list[i].Specialty = sv.DurationMinutes, sv.IsOther, sv.Name, sv.Specialty
		}
		p.Services = &list
	}
	res, err := s.DB.ServiceImport(ctx, p)
	if err != nil {
		return nil, mapErr(err)
	}
	if !dry {
		s.audit(ctx, "services_imported", nil)
	}
	return api.ImportServices200JSONResponse(api.ImportResult(*res)), nil
}

// ---- doctors

func (s *Server) ListDoctors(ctx context.Context, req api.ListDoctorsRequestObject) (api.ListDoctorsResponseObject, error) {
	l, err := s.DB.DoctorList(ctx, req.Params.SpecialtyId)
	if err != nil {
		return nil, mapErr(err)
	}
	out := api.DoctorList{Items: make([]api.Doctor, len(l.Items))}
	for i, d := range l.Items {
		sp := make([]api.Specialty, len(d.Specialties))
		for j, x := range d.Specialties {
			sp[j] = api.Specialty{Id: x.Id, Name: x.Name}
		}
		out.Items[i] = api.Doctor{Id: d.Id, FirstName: d.FirstName, LastName: d.LastName, Specialties: sp}
	}
	return api.ListDoctors200JSONResponse(out), nil
}

func scheduleOf(s mwclient.Schedule) api.Schedule {
	days := make([]api.WorkingDay, len(s.WorkingDays))
	for i, d := range s.WorkingDays {
		days[i] = api.WorkingDay{Weekday: d.Weekday, StartTime: d.StartTime, EndTime: d.EndTime}
	}
	return api.Schedule{SpecialtyIds: s.SpecialtyIds, WorkingDays: days}
}

func (s *Server) GetDoctorSchedule(ctx context.Context, req api.GetDoctorScheduleRequestObject) (api.GetDoctorScheduleResponseObject, error) {
	sch, err := s.DB.DoctorGetSchedule(ctx, req.DoctorId)
	if err != nil {
		return nil, mapErr(err)
	}
	return api.GetDoctorSchedule200JSONResponse(scheduleOf(*sch)), nil
}

// UpdateDoctorSchedule replaces specialties and working days (UF-09). Future bookings now outside the working time
// stay untouched and are listed in the answer.
func (s *Server) UpdateDoctorSchedule(ctx context.Context, req api.UpdateDoctorScheduleRequestObject) (api.UpdateDoctorScheduleResponseObject, error) {
	b := req.Body
	days := make([]mwclient.WorkingDay, len(b.WorkingDays))
	for i, d := range b.WorkingDays {
		days[i] = mwclient.WorkingDay{Weekday: d.Weekday, StartTime: d.StartTime, EndTime: d.EndTime}
	}
	res, err := s.DB.DoctorSetSchedule(ctx, mwclient.DoctorScheduleParams{DoctorId: req.DoctorId, SpecialtyIds: b.SpecialtyIds, WorkingDays: days})
	if err != nil {
		if db.Status(err) == http.StatusUnprocessableEntity && db.Code(err) == "unknown_reference" {
			return nil, unprocessable("unknown_specialty", "A specialty does not exist")
		}
		return nil, mapErr(err)
	}
	s.audit(ctx, "schedule_changed", &req.DoctorId)
	return api.UpdateDoctorSchedule200JSONResponse{Schedule: scheduleOf(res.Schedule), BookingsOutsideHours: res.BookingsOutsideHours}, nil
}

// ---- patients

func patientOf(p mwclient.Patient) api.Patient {
	return api.Patient{Id: p.Id, FirstName: p.FirstName, LastName: p.LastName, Email: p.Email, Phone: p.Phone, MainDoctorId: p.MainDoctorId}
}

func (s *Server) ListPatients(ctx context.Context, req api.ListPatientsRequestObject) (api.ListPatientsResponseObject, error) {
	page, err := s.DB.PatientList(ctx, mwclient.PatientListParams{Q: req.Params.Q, Limit: req.Params.Limit, Offset: req.Params.Offset})
	if err != nil {
		return nil, mapErr(err)
	}
	out := api.PatientPage{Items: make([]api.Patient, len(page.Items)), Total: page.Total}
	for i, p := range page.Items {
		out.Items[i] = patientOf(p)
	}
	return api.ListPatients200JSONResponse(out), nil
}

func (s *Server) SetMainDoctor(ctx context.Context, req api.SetMainDoctorRequestObject) (api.SetMainDoctorResponseObject, error) {
	p, err := s.DB.PatientSetMainDoctor(ctx, req.PatientId, req.Body.DoctorId)
	if err != nil {
		return nil, mapErr(err)
	}
	s.audit(ctx, "main_doctor_changed", &req.PatientId)
	return api.SetMainDoctor200JSONResponse(patientOf(*p)), nil
}
