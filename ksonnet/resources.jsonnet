
//
// Definition for input loader
//

// Import KSonnet library.
local k = import "ksonnet.beta.2/k.libsonnet";

// Short-cuts to various objects in the KSonnet library.
local depl = k.extensions.v1beta1.deployment;
local container = depl.mixin.spec.template.spec.containersType;
local resources = container.resourcesType;
local env = container.envType;
local svc = k.core.v1.service;
local svcPort = svc.mixin.spec.portsType;
local annotations = depl.mixin.spec.template.metadata.annotations;
local hpa = k.autoscaling.v1.horizontalPodAutoscaler;

local worker(config) = {

    local version = import "version.jsonnet",

    name: "analytics-input",
    images: ["gcr.io/trust-networks/analytics-input:" + version],

    local name = self.name,

    input: "",
    output: config.workers.queues.input.output,

    // Environment variables
    envs:: [
        // Hostname of Cherami
        env.new("CHERAMI_FRONTEND_HOST", "cherami")
    ],

    // Container definition.
    containers:: [
        container.new(name, self.images[0]) +
            container.env(self.envs) +
            container.args(std.map(function(x) "output:/queue/" + x,
                                   self.output)) +
            container.mixin.resources.limits({
                memory: "64M", cpu: "1.0"
            }) +
            container.mixin.resources.requests({
                memory: "64M", cpu: "0.7"
            })
    ],

    // Deployment definition.  id is the node ID.
    deployments:: [
        depl.new(self.name,
				config.workers.replicas.input.min,
                 self.containers,
                 {app: name, component: "analytics"}) +
	annotations({"prometheus.io/scrape": "true",
		     "prometheus.io/port": "8080"})
    ],

    // Front door service for tcp connections
    svcPorts:: [
        svcPort.newNamed("input", 48879, 48879) +
            svcPort.protocol("TCP")
    ],

    services:: [
        svc.new(name, {app: name}, self.svcPorts)
    ],

	autoScalers:: [
		hpa.new() +
		hpa.mixin.metadata.name("analytics-input") +
		hpa.mixin.metadata.labels({app: "analytics-input",
			component: "analytics"}) +
		hpa.mixin.spec.minReplicas(config.workers.replicas.input.min) +
		hpa.mixin.spec.maxReplicas(config.workers.replicas.input.max) +
		hpa.mixin.spec.targetCpuUtilizationPercentage(35) +
		hpa.mixin.spec.scaleTargetRef.name("analytics-input") +
		{spec+:{scaleTargetRef+:{kind:"Deployment",apiVersion:"apps/v1beta1"}}}

	],

	resources:
		if config.options.includeAnalytics then
			self.deployments + self.autoScalers + self.services
		else [],

};
worker
