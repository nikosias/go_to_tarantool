local log = require('log')
local fiber = require('fiber')
local metrics = require('metrics')
local http_server = require('http.server')
local prometheus = require('metrics.plugins.prometheus')

local server,err = http_server.new('0.0.0.0', 6081)
if err then
    log.error("Failed to create HTTP server: %s", err)
    os.exit(1)
end
server:start()

metrics.enable_default_metrics()

server:route({path = '/metrics'}, prometheus.collect_http)

box.cfg {listen = 3301}

box.once("bootstrap", function()
    -- Create a new space named "test"
    box.schema.create_space('test', { if_not_exists = true })

    -- Define the space format
    box.space.test:format({
        {name = 'id', type = 'unsigned'},
        {name = 'value', type = 'string'}
    })

    -- Create a primary key for the space
    box.space.test:create_index('primary', {
        parts = {'id'}
    })
    
    log.info("Space 'test' created")
end)

fiber.create(function() 
    while true do
        log.info("spase test count: %s", box.space.test:count())
        fiber.sleep(5)
    end
end)