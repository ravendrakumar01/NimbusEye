package oci

import (
	"strings"

	"nimbuseye/internal/model"
)

// typeMap translates OCI Resource Search resource types into NimbusEye catalog
// codes.
//
// Only mapped types become monitors. An unmapped type is reported by the
// collector rather than silently dropped, because "OCI has a service we do not
// model" is a gap worth seeing, not an error to swallow.
var typeMap = map[string]string{
	"Instance":            "OCI_COMPUTE_INSTANCE",
	"instance":            "OCI_COMPUTE_INSTANCE",
	"Bucket":              "OCI_OBJECT_STORAGE_BUCKET",
	"bucket":              "OCI_OBJECT_STORAGE_BUCKET",
	"Volume":              "OCI_BLOCK_VOLUME",
	"BootVolume":          "OCI_BLOCK_VOLUME",
	"AutonomousDatabase":  "OCI_AUTONOMOUS_DB",
	"DbSystem":            "OCI_DB_SYSTEM",
	"Database":            "OCI_DB_SYSTEM",
	"LoadBalancer":        "OCI_LOAD_BALANCER",
	"NetworkLoadBalancer": "OCI_LOAD_BALANCER",
	// Resource Search reports OKE clusters as "ClustersCluster", not "Cluster".
	// The obvious guess was wrong and silently hid every Kubernetes cluster in the
	// tenancy, which is exactly the failure mode the unmapped-types report exists
	// to surface.
	"ClustersCluster":   "OCI_OKE_CLUSTER",
	"Cluster":           "OCI_OKE_CLUSTER",
	"FunctionsFunction": "OCI_FUNCTION",
	"Vcn":               "OCI_VCN",
	"FileSystem":        "OCI_FILE_STORAGE",
	"MountTarget":       "OCI_FILE_STORAGE",
}

// ignoredTypes are resource types that exist in a tenancy but are not
// infrastructure to monitor: identity objects, configuration records, backups and
// artifacts.
//
// They are skipped silently rather than counted as unmapped. Without this the
// unmapped report is dominated by noise — one tenancy returned 14,243 container
// images against 146 monitorable resources — and a genuinely missing service type
// is impossible to spot in the list.
var ignoredTypes = map[string]bool{
	// Container registry artifacts.
	"ContainerImage": true, "ContainerRepo": true, "Image": true,
	// Identity and governance.
	"Compartment": true, "Policy": true, "Group": true, "User": true,
	"IdentityProvider": true, "Tag": true, "TagNamespace": true,
	"LimitsIncreaseRequest": true,
	// Network attachments and address objects, not independently monitorable.
	"Vnic": true, "PrivateIp": true, "PublicIp": true, "Subnet": true,
	"RouteTable": true, "SecurityList": true, "NetworkSecurityGroup": true,
	"DrgAttachment": true, "DrgRouteTable": true, "DrgRouteDistribution": true,
	"DnsResolver": true, "DnsView": true, "CustomerDnsZone": true,
	"Cpe": true, "IPSecConnection": true,
	// Backups and snapshots.
	"BootVolumeBackup": true, "VolumeBackup": true, "VolumeBackupPolicy": true,
	"AutonomousDatabaseBackup": true, "DbBackup": true,
	// Configuration and templates.
	"InstanceConfiguration": true, "InstancePool": true,
	"UnifiedAgentConfiguration": true, "ResourceSchedule": true,
	"OrmStack": true, "OrmJob": true, "Export": true,
	"ConsoleDashboard": true, "ConsoleDashboardGroup": true,
	// Logging, security and mail configuration.
	"Log": true, "LogGroup": true, "Alarm": true, "ServiceConnector": true,
	"CloudGuardTarget": true, "CloudGuardDetectorRecipe": true,
	"CloudGuardResponderRecipe": true, "CloudGuardManagedList": true,
	"VssHostScanRecipe": true, "VssHostScanTarget": true,
	"DataSafeAuditPolicy": true, "DataSafeAuditProfile": true,
	"DataSafeAuditTrail": true, "DataSafeSecurityAssessment": true,
	"DataSafeUserAssessment": true,
	"EmailDomain":            true, "EmailDkim": true, "EmailSender": true,
	"Key": true, "VaultSecret": true,
	"OsmhProfile": true, "OsmhManagedInstanceGroup": true,
	"OsmhScheduledJob": true, "OsmhSoftwareSource": true,
	"PathAnalyzerTest": true,
}

// IsIgnored reports whether a type is deliberately not monitored.
func IsIgnored(ociType string) bool { return ignoredTypes[ociType] }

// MapType returns the catalog code for an OCI resource type.
func MapType(ociType string) (string, bool) {
	if code, ok := typeMap[ociType]; ok {
		return code, true
	}
	// Resource Search is not perfectly consistent about casing across services.
	for k, v := range typeMap {
		if strings.EqualFold(k, ociType) {
			return v, true
		}
	}
	return "", false
}

// lifecycleToStatus maps an OCI lifecycle state onto a monitoring status.
//
// Three distinctions matter here, and getting any of them wrong produces either
// false alarms or false confidence:
//
//   - STOPPED is not DOWN. A stopped instance or a paused clone database is
//     intentionally off. One tenancy had 27 of them, mostly old clones and dev
//     machines; reporting those as down meant 27 availability alarms about
//     decisions someone made deliberately. They map to suspended, which is
//     retained and listed but never alerted on.
//   - TERMINATED means the resource no longer exists. It is reported as such so
//     the collector can retire it rather than monitoring a ghost.
//   - RUNNING is not UP. The control plane saying an instance exists is not an
//     availability check; a host can be wedged for an hour while OCI still
//     reports RUNNING. It maps to unknown, and is promoted to up only once the
//     resource actually reports a metric.
func lifecycleToStatus(state string) string {
	switch strings.ToUpper(state) {
	case "RUNNING", "AVAILABLE", "ACTIVE":
		return model.StatusUnknown
	case "STOPPED", "STOPPING", "INACTIVE", "PAUSED":
		return model.StatusSuspended
	case "TERMINATED", "DELETED", "TERMINATING", "DELETING":
		return statusGone
	case "FAILED", "UNAVAILABLE":
		return model.StatusDown
	case "PROVISIONING", "CREATING", "STARTING", "UPDATING", "RESTORING", "SCALE_IN_PROGRESS":
		return model.StatusDiscovery
	default:
		return model.StatusUnknown
	}
}

// statusGone is an internal marker: the resource no longer exists in the cloud,
// so it should be retired rather than stored with a status.
const statusGone = "__gone__"

// IsGone reports whether a discovered resource has been terminated and should not
// be monitored.
func IsGone(d DiscoveredResource) bool {
	return lifecycleToStatus(d.LifecycleState) == statusGone
}

// ToResource converts a discovered OCI resource into a NimbusEye resource.
// Returns false when the type is not in the catalog.
func ToResource(d DiscoveredResource, tenantID, accountID string) (model.Resource, bool) {
	code, ok := MapType(d.OCIResourceType)
	if !ok {
		return model.Resource{}, false
	}

	name := d.DisplayName
	if name == "" {
		name = shortOCID(d.OCID)
	}

	tags := map[string]string{}
	for k, v := range d.FreeformTags {
		tags[k] = v
	}
	// Defined tags arrive namespaced; flatten them the way the reference product
	// does, as "Namespace.Key", so cloud-imported and user tags share one shape.
	for ns, kv := range d.DefinedTags {
		for k, v := range kv {
			if s, isStr := v.(string); isStr {
				tags[ns+"."+k] = s
			}
		}
	}

	attrs := map[string]any{
		"oci_resource_type": d.OCIResourceType,
		"compartment_id":    d.CompartmentID,
		"lifecycle_state":   d.LifecycleState,
	}
	if d.AvailabilityDomain != "" {
		attrs["availability_domain"] = d.AvailabilityDomain
	}
	if d.TimeCreated != nil {
		attrs["created_at"] = d.TimeCreated.UTC()
	}

	// Derived once and used twice. The status column and the suspended flag must
	// agree: the alert evaluator reads the flag, so a resource that is
	// intentionally stopped keeps alerting if only the status is set.
	status := lifecycleToStatus(d.LifecycleState)

	return model.Resource{
		CloudAccountID:   accountID,
		Provider:         "oci",
		ResourceType:     code,
		NativeID:         d.OCID,
		DisplayName:      name,
		Region:           d.Region,
		AvailabilityZone: d.AvailabilityDomain,
		Status:           status,
		Suspended:        status == model.StatusSuspended,
		Tags:             tags,
		Attributes:       attrs,
	}, true
}

// shortOCID produces a readable fallback name from an OCID.
func shortOCID(ocid string) string {
	parts := strings.Split(ocid, ".")
	if len(parts) < 2 {
		return ocid
	}
	last := parts[len(parts)-1]
	if len(last) > 12 {
		last = last[len(last)-12:]
	}
	return parts[1] + "-" + last
}
