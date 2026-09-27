// The greeter module (modules/greeter), written in TypeScript: the same
// capabilities and schemas, so nodes can run either.
//
//   modules:
//     - name: greeter
//       run: [node, examples/greeter-ts/greeter.ts]

import { serve } from '../../sdk/typescript/module.ts'

const greeting = {
  type: 'object',
  required: ['greeting'],
  properties: { greeting: { type: 'string' } },
}

const manifest = {
  name: 'greeter',
  version: '1.0.0',
  description: 'Says hello across the network. The TypeScript version.',
  capabilities: [
    {
      name: 'hello',
      description: 'Greets the caller.',
      access: 'network',
      input: { type: 'object', properties: { name: { type: 'string', description: 'Who to greet.' } } },
      output: greeting,
    },
    {
      name: 'greet',
      description: 'Asks the node with the given ID to greet this one.',
      input: {
        type: 'object',
        required: ['id'],
        properties: {
          id: { type: 'string', description: 'The node ID, in hex.' },
          name: { type: 'string', description: 'Who to be greeted as.' },
        },
      },
      output: greeting,
    },
  ],
  events: [
    {
      name: 'greeted',
      description: 'Someone was greeted.',
      schema: {
        type: 'object',
        required: ['name', 'caller'],
        properties: {
          name: { type: 'string' },
          caller: { type: 'string', description: 'The ID of the node that called.' },
        },
      },
    },
  ],
}

serve((config, env) => ({
  manifest,
  handlers: {
    hello: async (input, call) => {
      await env.emit('greeted', { name: input.name ?? '', caller: call.caller })
      return {
        greeting: `${config.greeting ?? 'Hello'} ${input.name ?? ''}, from ${env.nodeName}! You called from node ${call.caller.slice(0, 8)}. (TypeScript)`,
      }
    },
    greet: input => env.callNode(input.id, 'greeter.hello', { name: input.name }),
  },
}))
