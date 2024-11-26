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

local function apply()
    box.once("bootstrap", function()
        -- Create a sharding_space
        local sharding_space = box.schema.space.create('_ddl_sharding_key', {
            format = {
                {name = 'space_name', type = 'string', is_nullable = false},
                {name = 'sharding_key', type = 'array', is_nullable = false},
            },
            if_not_exists = true,
        })

        -- Create a primary key for the sharding_space
        sharding_space:create_index('space_name', {
            type = 'TREE',
            unique = true,
            parts = {{'space_name', 'string', is_nullable = false}},
            if_not_exists = true,
        })

        -- Create a new space named "test"
        box.schema.create_space('test', { if_not_exists = true })

        -- Define the space format
        box.space.test:format({
            {name = 'id', type = 'unsigned'},
            {name = 'bucket_id', type = 'unsigned'},
            {name = 'value', type = 'string'}
        })

        -- Create a primary key for the space
        box.space.test:create_index('primary', { parts = {'id'}})
        -- Create a bucket_id key for the space
        box.space.test:create_index('bucket_id', { parts = {'bucket_id'}, unique = false, if_not_exists = true})

        -- Add sharding key
        box.space._ddl_sharding_key:replace({'test', {'id'}})

        log.info("Space 'test' created")
    end)
end

return {
    validate = function()end,
    apply = apply,
    stop = function()end,
    depends = {'roles.crud-storage'},
}