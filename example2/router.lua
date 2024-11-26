local log = require('log')
local fiber = require('fiber')
local metrics = require('metrics')
local http_server = require('http.server')
local prometheus = require('metrics.plugins.prometheus')
local os = require('os')
local crud = require('crud')
local vshard = require('vshard')

local server,err = http_server.new('0.0.0.0', 6081)
if err then
    log.error("Failed to create HTTP server: %s", err)
    os.exit(1)
end
server:start()

metrics.enable_default_metrics()

server:route({path = '/metrics'}, prometheus.collect_http)

local function apply()
    while true do
        local ok, err = vshard.router.bootstrap({
            if_not_bootstrapped = true,
        })
        if ok then
            break
        end
        log.info(('Router bootstrap error: %s'):format(err))
        fiber.sleep(1)
    end
    crud.cfg({stats = true, stats_driver = 'metrics'})
    rawset(_G, "crud_get", function(space, index)
        return crud.get(space, index).rows
    end)

end

return {
    validate = function()end,
    apply = apply,
    stop = function()end,
    depends = {'roles.crud-router'},
}