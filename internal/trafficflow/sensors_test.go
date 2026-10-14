package trafficflow

import "testing"

func TestCreateSensorRequiresSourceUUIDAndLabel(t *testing.T) {
	setupTestDB(t)
	if _, err := CreateSensor(Sensor{Label: "no uuid"}); err == nil {
		t.Error("expected an error when source_uuid is empty")
	}
	if _, err := CreateSensor(Sensor{SourceUUID: "cam-1"}); err == nil {
		t.Error("expected an error when label is empty")
	}
}

func TestCreateSensorRejectsDuplicateSourceUUID(t *testing.T) {
	setupTestDB(t)
	if _, err := CreateSensor(Sensor{SourceUUID: "cam-1", Label: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateSensor(Sensor{SourceUUID: "cam-1", Label: "B"}); err == nil {
		t.Error("expected an error mapping the same source_uuid twice")
	}
}

func TestUpdateSensorPreservesCreatedAtAndAllowsRename(t *testing.T) {
	setupTestDB(t)
	sn, err := CreateSensor(Sensor{SourceUUID: "cam-1", Label: "A", Lat: 10, Lon: 106})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateSensor(sn.ID, Sensor{SourceUUID: "cam-1", Label: "B", Lat: 11, Lon: 107, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Label != "B" || updated.Lat != 11 {
		t.Errorf("expected update to apply, got %+v", updated)
	}
	if !updated.CreatedAt.Equal(sn.CreatedAt) {
		t.Error("expected CreatedAt to be preserved across update")
	}
}

func TestUpdateSensorRejectsSourceUUIDUsedByAnother(t *testing.T) {
	setupTestDB(t)
	_, err := CreateSensor(Sensor{SourceUUID: "cam-1", Label: "A"})
	if err != nil {
		t.Fatal(err)
	}
	sn2, err := CreateSensor(Sensor{SourceUUID: "cam-2", Label: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateSensor(sn2.ID, Sensor{SourceUUID: "cam-1", Label: "B"}); err == nil {
		t.Error("expected an error reassigning sn2 to a source_uuid already used by another sensor")
	}
}

func TestUpdateSensorUnknownIDFails(t *testing.T) {
	setupTestDB(t)
	if _, err := UpdateSensor("no-such-id", Sensor{SourceUUID: "cam-1", Label: "A"}); err != errTrafficFlowNotFound {
		t.Errorf("expected errTrafficFlowNotFound, got %v", err)
	}
}

func TestDeleteSensor(t *testing.T) {
	setupTestDB(t)
	sn, err := CreateSensor(Sensor{SourceUUID: "cam-1", Label: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteSensor(sn.ID); err != nil {
		t.Fatal(err)
	}
	if len(ListSensors()) != 0 {
		t.Error("expected no sensors left after delete")
	}
	if err := DeleteSensor(sn.ID); err != errTrafficFlowNotFound {
		t.Errorf("expected errTrafficFlowNotFound deleting an already-deleted sensor, got %v", err)
	}
}

func TestObserveSensorTracksCountAndCoordinates(t *testing.T) {
	setupTestDB(t)
	lat, lng := 10.123, 106.456
	observeSensor("cam-1", "hw-1", &lat, &lng)
	observeSensor("cam-1", "hw-1", &lat, &lng)
	observeSensor("cam-2", "", nil, nil)

	list := ListObservedSensors()
	if len(list) != 2 {
		t.Fatalf("expected 2 distinct observed sensors, got %d", len(list))
	}

	var cam1 *ObservedSensor
	for i := range list {
		if list[i].SourceUUID == "cam-1" {
			cam1 = &list[i]
		}
	}
	if cam1 == nil {
		t.Fatal("expected cam-1 to be observed")
	}
	if cam1.Count != 2 {
		t.Errorf("expected Count=2 for cam-1, got %d", cam1.Count)
	}
	if cam1.ReportedLat == nil || *cam1.ReportedLat != lat {
		t.Errorf("expected ReportedLat=%v, got %+v", lat, cam1.ReportedLat)
	}
}

func TestObserveSensorIgnoresEmptySourceUUID(t *testing.T) {
	setupTestDB(t)
	observeSensor("", "", nil, nil)
	if len(ListObservedSensors()) != 0 {
		t.Error("expected an empty source_uuid to be ignored, not recorded")
	}
}
