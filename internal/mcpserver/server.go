package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"mcpshka/internal/ownerweb"
	"mcpshka/internal/tools/approvedtask"
	"mcpshka/internal/tools/gitunlock"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/approval"
	"mcpshka/internal/artifacts"
	"mcpshka/internal/audit"
	"mcpshka/internal/authn"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/controlplane"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/gitops"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/laneprovider"
	"mcpshka/internal/mcptransport"
	"mcpshka/internal/mission"
	"mcpshka/internal/networkops"
	"mcpshka/internal/project"
	"mcpshka/internal/projectjobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/runnerhub"
	"mcpshka/internal/serverruntime"
	"mcpshka/internal/systemdsvc"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/autonomy"
	"mcpshka/internal/tools/processtools"
	"mcpshka/internal/toolstats"
	"mcpshka/internal/trafficcompat"
	"mcpshka/internal/verify"
)

const Version = "0.19.4"

func makeNetworkStageStatus(control controlplane.Snapshot, lane laneprovider.Status, laneErr error, network networkops.StateDigest, networkErr error) NetworkStageStatusResult {
	return toolcontract.MakeNetworkStageStatus(control, lane, laneErr, network, networkErr)
}
func leaseRecoveryStatus(status projectlease.Status, activity jobs.ProjectActivity) ProjectLeaseRecoveryStatus {
	return toolcontract.LeaseRecoveryStatus(status, activity)
}

// toolSchemaCache shares inferred and resolved tool schemas between server
// builds: inferring and resolving ~330 schemas took most of each New() (2-3 s
// on H96), which made internal/mcpserver hit the 10 min test limit.
var toolSchemaCache = mcp.NewSchemaCache()

func New(manager *jobs.Manager, files *filesystem.Service) *mcp.Server {
	return newServer(manager, files, artifacts.New(manager.Policy(), artifacts.DefaultTTL, artifacts.DefaultMaxActive))
}

func newServer(manager *jobs.Manager, files *filesystem.Service, artifactManager *artifacts.Manager) *mcp.Server {
	return newServerWithCoordinator(manager, files, artifactManager, newWorkflowCoordinator(manager))
}

func newServerWithCoordinator(manager *jobs.Manager, files *filesystem.Service, artifactManager *artifacts.Manager, c *workflowCoordinator) *mcp.Server {
	full, _ := newServers(manager, files, artifactManager, c)
	return full
}

// newServers builds the full server and the compact chat surface over one
// registry, so both share handlers, stores and gates.
func newServers(manager *jobs.Manager, files *filesystem.Service, artifactManager *artifacts.Manager, c *workflowCoordinator) (*mcp.Server, *mcp.Server) {
	if artifactManager == nil {
		artifactManager = artifacts.New(manager.Policy(), artifacts.DefaultTTL, artifacts.DefaultMaxActive)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "MCPshka", Version: Version}, &mcp.ServerOptions{Instructions: capabilityInstructions, HasTools: true, SchemaCache: toolSchemaCache})
	git := gitops.New(manager.Policy())
	projects := project.New(manager.Policy())
	profiles := projectprofile.New(manager.Policy())
	deploys := c.deploys
	verifier := verify.New(manager.Policy())
	leases := c.leases
	pipelines := c.pipelines
	guard := c.Guard
	pluginHub.Dir = filepath.Join(filepath.Dir(manager.StateDir()), "mcphub")
	idempotencyStore := idempotency.NewWithDatabase(filepath.Join(filepath.Dir(manager.StateDir()), "idempotency"), manager.Database())
	systemdServices := systemdsvc.NewFromEnvironment()
	dockerService := c.dockerService
	networkService := networkops.New()
	controlService := controlplane.New()
	laneService := laneprovider.New()
	approvalStore := manager.ApprovalStore()
	if approvalStore == nil {
		approvalStore = approval.New(filepath.Join(filepath.Dir(manager.StateDir()), "approvals"))
		manager.SetApprovalStore(approvalStore)
	}
	registry := &toolRegistry{audit: manager.AuditLogger(), server: s, store: idempotencyStore, approvals: approvalStore, leases: leases, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	registry.clients = newClientCatalog(filepath.Join(filepath.Dir(manager.StateDir()), "client-catalog.json"))
	s.AddReceivingMiddleware(registry.clients.Middleware)
	s.AddReceivingMiddleware(failureMetadata)
	s.AddReceivingMiddleware(schemaHints(registry))
	registerContractTools(registry)
	registerRootTaskTools(registry, files)
	registerAdminSessionTools(registry, files, manager.Database())
	registerInteractiveFenceRepairTools(registry, manager)
	registerStaleLeaseTools(registry, manager)
	registerStalePermitTools(registry)
	registerPhaseGateTool(registry, manager)
	registerIssueTools(registry, manager)
	registerHostTriageTool(registry, dockerService, systemdServices, manager, c.selfHeal)
	registerSelfHealTool(registry, c.selfHeal)
	registerHATools(registry, idempotencyStore)
	registerGuideTool(registry)
	registerProvidersTool(registry)
	registerGitProviderTools(registry)
	registerGitProviderPush(registry, manager)
	registerToolUsage(registry, manager.Database())
	autonomy.Register(registry.install, manager)
	registerSalvageTool(registry, git)
	registerHostRebootPlan(registry, manager)
	registerRoadmapTools(registry, manager)
	registerProjectRepoTools(registry)
	registerReleaseApplyTools(registry, manager.Database())
	registerReleaseQueue(registry, manager, git)
	registerDiagReleaseTools(registry, manager)
	registerProjectRelease(registry)
	registerBrowserTools(registry, manager)
	registerDocTools(registry, manager)
	registerClientCatalogTool(registry)
	registerAgentTools(registry, manager)
	gitunlock.Register(registry.install, registry.store, manager, leases)
	registerRescueTools(registry)
	registerCodexTools(registry, idempotencyStore)
	registerCodexIntentTools(registry, manager.Database())
	registerCodexQueueTools(registry, manager.Database())
	registerCodexAdmissionTools(registry)
	registerCodexRoutePreview(registry)
	registerDockerTerminalTools(registry)
	registerStateTools(registry, manager, idempotencyStore)
	registerPipelineTools(registry, pipelines)
	integrations := c.integrations
	registerIntegrationTools(registry, integrations)
	registerIntegrationAdminTools(registry, integrations)
	registerExecutorFenceTools(registry, manager)
	registerExecutorRuntimeStatus(registry, manager)
	registerIntegrationRecoveryTools(registry, manager)
	registerIntegrationWorkerTools(registry, manager)
	registerIntegrationPublishTool(registry, manager, c.Guard, integrations)
	registerProjectContext(registry, manager)
	registerCloudFanIn(registry)
	registerCloudAgent(registry)
	registerCloudTaskStatus(registry, manager.Database())
	registerCloudDispatch(registry, manager.Database())
	registerCloudCodex(registry)
	registerWorkYield(registry, manager, leases)
	registerRecoveryDiagnosis(registry, manager, leases)
	healthCollector := newHealthCollector(manager, controlService, systemdServices, dockerService, laneService, registry, c.selfHeal)
	missionReader := newMissionReader(manager)
	missionController := mission.NewController(manager.Database(), git, func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		reader := registry.lookupTool(name).reader
		if reader == nil {
			return nil, fmt.Errorf("read-only mission dependency unavailable: %s", name)
		}
		out, err := reader(ctx, args)
		if err != nil {
			return nil, err
		}
		return object(out)
	})
	missionController.WorkflowControl = func(id, task, mode string, deadline *time.Time) error {
		if err := c.pipelines.BindMission(id, task, deadline); err != nil {
			return err
		}
		if mode == "bind" {
			return nil
		}
		return c.pipelines.ControlMission(id, task, mode)
	}
	missionController.WorkflowDispatch = func(plan projectprofile.PipelinePin, task string, deadline *time.Time, snapshot []byte) error {
		_, err := c.pipelines.StartMission(plan, task, deadline, snapshot)
		return err
	}
	c.pipelines.OnTerminal = missionController.WorkflowCompleted
	c.missions = missionController
	ownerweb.ToolCall = ownerWebToolCall(registry)
	registerRunnerTools(registry, runnerhub.Default)
	wireOffbox(manager)
	registerMissionTools(registry, missionReader, missionController)
	registerMissionControllerTools(registry, missionController, idempotencyStore)
	registerHealthTools(registry, healthCollector)
	registerOwnerPortal(registry, healthCollector, manager.Database())
	registerTaskPlan(registry)
	registry.health = healthCollector
	registerKnowledgeDIAG(registry, manager)
	registerDIAGObserver(registry)
	registerStorage(registry, manager)
	registerStorageConnect(registry, manager)
	registerDockerSmoke(registry, manager)
	registerKnowledgeLocal(registry, manager)
	registerMemory(registry, manager)
	registerBrain(registry, manager.StateDir())
	inbox := artifactInbox(manager)
	registerArtifactUploads(registry, manager, inbox)
	registerReleases(registry, manager, idempotencyStore)
	registerForgeCI(registry, manager, idempotencyStore)
	deps := &serverDeps{manager: manager, files: files, artifactManager: artifactManager, git: git, projects: projects,
		profiles: profiles, deploys: deploys, verifier: verifier, leases: leases, pipelines: pipelines, guard: guard,
		idempotencyStore: idempotencyStore, systemdServices: systemdServices, dockerService: dockerService,
		networkService: networkService, controlService: controlService, laneService: laneService,
		approvalStore: approvalStore, healthCollector: healthCollector, inbox: inbox, integrations: integrations, c: c, s: s}
	registerSystemTools(registry, deps)
	registerNetworkTools(registry, deps)
	registerApprovalTools(registry, deps)
	registerServiceTools(registry, deps)
	registerDockerTools(registry, deps)
	registerJobTools(registry, deps)
	processtools.Register(registry.install, manager, uploadOwner, testRunnerRoute)
	registerFilesystemTools(registry, deps)
	registerHostTools(registry, deps)
	registerProjectTools(registry, deps)
	registerLeaseTools(registry, deps)
	registerDeployTools(registry, deps)
	registerCapabilityTools(registry, manager.Policy())
	registerIntegrationShip(registry, integrations)
	registry.validate()
	return s, newCoreServer(registry)
}

type trafficRouteCompatPlan = trafficcompat.Plan

var parseTrafficRouteCompat = trafficcompat.Parse

func runTrafficRouteCompat(ctx context.Context, manager *jobs.Manager, store *idempotency.Store, in ExecStartInput, plan trafficRouteCompatPlan) (jobs.Snapshot, error) {
	return trafficcompat.Run(ctx, manager, store, in, plan)
}

func runIdempotent[T any](store *idempotency.Store, key, operation string, payload any, fn func() (T, error)) (T, error) {
	return idempotency.Run(store, key, operation, payload, fn)
}

const (
	goTestMemoryLimit  = projectjobs.TestMemoryLimit
	goTestCPUTimeLimit = projectjobs.TestCPUTimeLimit
)

var projectJobOptions = projectjobs.Options
var profileJobOptions = projectjobs.ProfileOptions

func RunStdio(ctx context.Context) error {
	manager, files, coordinator, stop, err := newServicesFromEnv()
	if err != nil {
		return err
	}
	defer stop()
	return newServerWithCoordinator(manager, files, nil, coordinator).Run(ctx, &mcp.StdioTransport{})
}

func RunHTTP(ctx context.Context, addr, token string) error {
	if addr == "" {
		addr = "127.0.0.1:8765"
	}
	if err := requireLoopbackAddr(addr); err != nil {
		return err
	}
	authFile := strings.TrimSpace(os.Getenv("MCPSHKA_AUTH_FILE"))
	if authFile == "" && strings.TrimSpace(token) == "" {
		return errors.New("MCPSHKA_TOKEN or MCPSHKA_AUTH_FILE is required for mcp-http")
	}
	authenticator, err := authn.New(authFile, token)
	if err != nil {
		return fmt.Errorf("configure MCP HTTP authentication: %w", err)
	}

	manager, files, coordinator, stop, err := newServicesFromEnv()
	if err != nil {
		return err
	}
	defer stop()
	// Lifetime tool usage (MCP-151): only the long-lived HTTP server owns the ledger.
	defer toolstats.Default.Persist(filepath.Join(filepath.Dir(manager.StateDir()), "tool-usage.json"), time.Minute)()

	mainHandler, mcpHandler := httpHandlers(manager, files, authenticator, artifacts.New(manager.Policy(), artifacts.DefaultTTL, artifacts.DefaultMaxActive), coordinator)
	// Optional Face ID portal has a separate socket. MCP is reachable there
	// only through the OAuth connector (MCPSHKA_OAUTH_ISSUER), never with the
	// static bearer tokens. A missing origin disables the listener entirely.
	ownerweb.ContinueApproval, ownerweb.IssuesAPI, ownerweb.IssuesPage = approvedtask.ContinuePortal, issuesAPI, issuesPage
	ownerweb.MemoryDir, ownerweb.Plugins = manager.StateDir(), pluginHub
	connector, err := ownerweb.NewOAuthServer(manager.Database(), authenticator, mcpHandler)
	if err != nil {
		return fmt.Errorf("OAuth connector setup: %w", err)
	}
	portalListener, err := ownerweb.OpenPasskeyListener()
	if err != nil {
		return fmt.Errorf("passkey listener setup: %w", err)
	}
	var portalHandler http.Handler
	if portalListener != nil {
		portalHandler = ownerweb.PasskeyOnlyHandler(connector)
	}
	return mcptransport.Serve(ctx, addr, mainHandler, portalListener, portalHandler)
}

func HTTPHandler(manager *jobs.Manager, args ...any) http.Handler {
	files := filesystem.NewWithAudit(manager.Policy(), manager.AuditLogger())
	token := ""
	switch len(args) {
	case 1:
		token, _ = args[0].(string)
	case 2:
		if supplied, ok := args[0].(*filesystem.Service); ok && supplied != nil {
			files = supplied
		}
		token, _ = args[1].(string)
	}
	artifactManager := artifacts.New(manager.Policy(), artifacts.DefaultTTL, artifacts.DefaultMaxActive)
	return httpHandlerWithArtifacts(manager, files, token, artifactManager)
}

func httpHandlerWithArtifacts(manager *jobs.Manager, files *filesystem.Service, token string, artifactManager *artifacts.Manager) http.Handler {
	authenticator, err := authn.New("", token)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	return httpHandlerWithAuthenticator(manager, files, authenticator, artifactManager)
}

func httpHandlerWithAuthenticator(manager *jobs.Manager, files *filesystem.Service, authenticator *authn.Authenticator, artifactManager *artifacts.Manager) http.Handler {
	return httpHandlerWithCoordinator(manager, files, authenticator, artifactManager, newWorkflowCoordinator(manager))
}

func httpHandlerWithCoordinator(manager *jobs.Manager, files *filesystem.Service, authenticator *authn.Authenticator, artifactManager *artifacts.Manager, c *workflowCoordinator) http.Handler {
	mux, _ := httpHandlers(manager, files, authenticator, artifactManager, c)
	return mux
}

// httpHandlers returns the loopback API and the bare MCP handler the OAuth
// connector mounts behind its own token check on the public portal.
func httpHandlers(manager *jobs.Manager, files *filesystem.Service, authenticator *authn.Authenticator, artifactManager *artifacts.Manager, c *workflowCoordinator) (*http.ServeMux, http.Handler) {
	full, core := newServers(manager, files, artifactManager, c)
	streamHandler := mcp.NewStreamableHTTPHandler(
		pickServer(full, core),
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)

	mcpHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("tools") == "all" {
			req = req.WithContext(context.WithValue(req.Context(), fullCatalogKey{}, true))
		}
		streamHandler.ServeHTTP(w, req)
	})

	return mcptransport.Mux(Version, authenticator, artifactManager, mcpHandler), mcpHandler
}

func newServicesFromEnv() (*jobs.Manager, *filesystem.Service, *workflowCoordinator, func(), error) {
	manager, files, stopServices, err := serverruntime.NewFromEnvironment()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	logger := manager.AuditLogger()
	coordinator := newWorkflowCoordinator(manager)
	coordinator.leases.MaintainManaged(manager, coordinator.deploys)
	stopMaintenance := startWorkflowMaintenance(10*time.Second, coordinator.Maintain, func(err error) {
		_ = logger.Log(audit.Event{Type: "workflow_maintenance", Result: err.Error()})
	})
	stopSelfHeal := coordinator.selfHeal.Start(func(err error) {
		_ = logger.Log(audit.Event{Type: "self_heal_pass", Result: err.Error()})
	})
	stopExecutorHeal := func() {}
	if brokerclient.Enabled() {
		stopExecutorHeal = startExecutorSelfHeal(3*time.Minute, 15*time.Minute, executorSelfHealPass, func(n int, err error) {
			if err != nil {
				_ = logger.Log(audit.Event{Type: "executor_self_heal", Result: err.Error()})
			} else if n > 0 {
				_ = logger.Log(audit.Event{Type: "executor_self_heal", Result: fmt.Sprintf("fenced %d proven-abandoned permits", n)})
			}
		})
	}
	stop := func() {
		stopExecutorHeal()
		stopSelfHeal()
		if err := stopMaintenance(5 * time.Second); err != nil {
			_ = logger.Log(audit.Event{Type: "workflow_maintenance_shutdown", Result: err.Error()})
		}
		stopServices()
	}
	return manager, files, coordinator, stop, nil
}

func requireLoopbackAddr(addr string) error { return mcptransport.RequireLoopbackAddr(addr) }

// rawJSONSchemas describes embedded raw JSON as any JSON value.
var rawJSONSchemas = tooldef.RawJSONSchemas
