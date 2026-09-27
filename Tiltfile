# Dev environment: runs the network lab from compose.yaml, rebuilds the nodes
# when Go code changes, and serves the network explorer (web/) with hot reload.
#
#   tilt up

# The name must match compose.yaml's, tag included, for the services to pick
# up each build.
docker_build(
    'go-decentralized/node:dev',
    '.',
    only=['go.mod', 'go.sum', 'cmd', 'internal', 'module', 'modules'],
)
docker_compose('compose.yaml')

# Group the lab by network in the Tilt UI.
networks = {
    'relay': 'saas',
    'corp-router': 'corporate',
    'kg-host': 'corporate',
    'home-router': 'home',
    'ks-host': 'home',
}
for org, network in [('gl', 'saas'), ('kg', 'corporate'), ('ks', 'home')]:
    networks[org] = network
    for project in ['project-1', 'project-2', 'project-3']:
        networks[org + '-' + project] = network
for service, network in networks.items():
    dc_resource(service, labels=[network])

local_resource(
    'explorer',
    cmd='npm install',
    dir='web',
    deps=['web/package.json'],
    serve_cmd='npm run dev',
    serve_dir='web',
    links=[link('http://localhost:5173', 'Network explorer')],
    labels=['explorer'],
    resource_deps=['gl'],
)
