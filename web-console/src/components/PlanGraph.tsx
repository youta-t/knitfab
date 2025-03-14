import { Collapse, Stack } from "@mui/material";
import Box from "@mui/material/Box";
import {
    Background,
    Controls,
    Edge,
    Handle,
    Node,
    NodeProps,
    Position,
    ReactFlow,
    ReactFlowProvider,
    useEdgesState,
    useNodesState,
    useReactFlow,
} from "@xyflow/react";
import React, { useEffect } from "react";
import { PlanService } from "../api/services/planService";
import { getLayoutedNodes } from "../dag";
import { Input, Log, Output, PlanDetail } from "../types/types";
import { InputPointCard, LogPointCard, OutputPointCard, PlanCard, PlanItem } from "./Items";

type NodeValues = {
    plan: PlanDetail,
    variant?: "outlined" | "elevation",
    onClick: (plan: PlanDetail) => void,
    onEnter: (planId: string) => void,
    onLeave: (planId: string) => void,
}

type PlanNodeValues = NodeValues;

const PlanNode: React.FC<NodeProps<Node<PlanNodeValues, "planNode">>> = ({ data }) => {
    const { plan, variant = "outlined", } = data;

    return (
        <>
            {
                0 < plan.inputs.length &&
                <Handle type="target" position={Position.Top} isConnectable={false} />
            }
            <Box
                maxWidth="33vw"
                onClick={(ev) => {
                    ev.stopPropagation();
                    data.onClick(plan);
                }}
                onMouseEnter={() => data.onEnter(plan.planId)}
                onMouseLeave={() => data.onLeave(plan.planId)}
            >
                <PlanCard variant={variant} plan={plan} elevation={8} />
            </Box>
            {
                (0 < plan.outputs.length || plan.log) &&
                <Handle type="source" position={Position.Bottom} isConnectable={false} />
            }
        </>
    );
}

type InputNodeaValues = NodeValues & { input: Input }
const InputNode: React.FC<NodeProps<Node<InputNodeaValues, "inputNode">>> = ({ data }) => {
    const { plan, input, variant = "outlined" } = data;

    return (
        <>
            {
                0 < input.upstreams.length &&
                <Handle type="target" position={Position.Top} isConnectable={false} />
            }
            <Box
                maxWidth="33vw"
                onClick={(ev) => {
                    ev.stopPropagation();
                    data.onClick(plan);
                }}
                onMouseEnter={() => data.onEnter(plan.planId)}
                onMouseLeave={() => data.onLeave(plan.planId)}
            >
                <InputPointCard variant={variant} mountpoint={input} elevation={8} />
            </Box>
            <Handle type="source" position={Position.Bottom} isConnectable={false} />
        </>
    );
}

type OutputNodeValues = NodeValues & { output: Output }

const OutputNode: React.FC<NodeProps<Node<OutputNodeValues, "outputNode">>> = ({ data }) => {
    const { plan, output, variant = "outlined" } = data;

    return (
        <>
            <Handle type="target" position={Position.Top} isConnectable={false} />
            <Box
                maxWidth="33vw"
                onClick={(ev) => {
                    ev.stopPropagation();
                    data.onClick(plan);
                }}
                onMouseEnter={() => data.onEnter(plan.planId)}
                onMouseLeave={() => data.onLeave(plan.planId)}
            >
                <OutputPointCard mountpoint={output} variant={variant} elevation={8} />
            </Box>
            {
                0 < output.downstreams.length &&
                <Handle type="source" position={Position.Bottom} isConnectable={false} />
            }
        </>
    );
}

type LogNodeValues = NodeValues & { log: Log }

const LogNode: React.FC<NodeProps<Node<LogNodeValues, "logNode">>> = ({ data }) => {
    const { plan, log, variant = "outlined" } = data;

    return (
        <>
            <Handle type="target" position={Position.Top} isConnectable={false} />
            <Box
                maxWidth="33vw"
                onClick={(ev) => {
                    ev.stopPropagation();
                    data.onClick(plan);
                }}
                onMouseEnter={() => data.onEnter(plan.planId)}
                onMouseLeave={() => data.onLeave(plan.planId)}
            >
                <LogPointCard variant={variant} log={log} elevation={8} />
            </Box >
            {
                0 < log.downstreams.length &&
                <Handle type="source" position={Position.Bottom} isConnectable={false} />
            }
        </>
    );
}

const nodeTypes = {
    planNode: PlanNode,
    inputNode: InputNode,
    outputNode: OutputNode,
    logNode: LogNode,
};

const PlanGraphInner: React.FC<{
    rootPlanId: string,
    planService: PlanService,
}> = ({ rootPlanId, planService }) => {
    type Link = {
        type: "input-to-plan" | "output-from-plan" | "output-to-input",
        source: string,
        target: string,
        weight?: number,
        style?: React.CSSProperties,
    }

    type NodeVariants = (
        { type: "planNode", data: PlanNodeValues }
        | { type: "inputNode", data: InputNodeaValues }
        | { type: "outputNode", data: OutputNodeValues }
        | { type: "logNode", data: LogNodeValues }
    )
    const [nodes, setNodes, onNodesChange] = useNodesState<Node & NodeVariants>([]);
    const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);
    const [selectedPlan, setSelectedPlan] = React.useState<PlanDetail | null>(null);
    const [selectedPlanIsExpandedd, setSelectedPlanIsExpanded] = React.useState<boolean>(false);

    const sameLink = (a: Link, b: Link) => {
        return a.type === b.type && a.source === b.source && a.target === b.target;
    };

    useEffect(() => {
        const fetchedPlan: { plan: PlanDetail }[] = [];
        const fetchedInputs: { plan: PlanDetail, input: Input }[] = [];
        const fetchedOutputs: { plan: PlanDetail, output: Output }[] = [];
        const fetchedLogs: { plan: PlanDetail, log: Log }[] = [];
        const fetchedLinks: Link[] = [];

        // recursive function to fetch Plan and its neighbors.
        //
        // Found items (Plan, Input, Output, Log) are stored in fetched* arrays.
        // Found links between items are stored in fetchedLinks array.
        const fetchPlan = async (planId: string) => {
            if (fetchedPlan.some((item) => item.plan.planId === planId)) { return; }

            const plan = await planService.fetchById(planId);
            fetchedPlan.push({ plan: plan });

            const inputs = plan.inputs.map((input) => {
                return { plan: plan, input: input };
            });
            fetchedInputs.push(...inputs);

            const outputs = plan.outputs.map((output) => {
                return { plan: plan, output: output };
            });
            fetchedOutputs.push(...outputs);

            if (plan.log) {
                fetchedLogs.push({ plan: plan, log: plan.log });
            }

            // Inner-Plan links (type = "input-to-plan", "output-from-plan", "output-to-input")
            //
            // They have higher weight and width than Inter-Plan links to make them more visible.
            const inputLinks: Link[] = plan.inputs.map((input) => {
                return {
                    type: "input-to-plan",
                    source: `${plan.planId}:${input.path}`,
                    target: plan.planId,
                    weight: 10,
                    style: { strokeWidth: 3 },
                };
            });
            const outputLinks: Link[] = plan.outputs.map((output) => {
                return {
                    type: "output-from-plan",
                    source: plan.planId,
                    target: `${plan.planId}:${output.path}`,
                    weight: 10,
                    style: { strokeWidth: 3 },
                };
            });
            const logLinks: Link[] = plan.log ? [{
                type: "output-from-plan",
                source: plan.planId,
                target: `${plan.planId}:log`,
                weight: 10,
                style: { strokeWidth: 3 },
            }] : [];

            fetchedLinks.push(...inputLinks, ...outputLinks, ...logLinks);

            // fetch neighbors Plans
            for (const input of plan.inputs) {
                for (const upstream of input.upstreams) {
                    await fetchPlan(upstream.plan.planId);

                    let suffix: string;
                    if (upstream.mountpoint) {
                        suffix = upstream.mountpoint.path;
                    } else if (upstream.log) {
                        suffix = "log";
                    } else {
                        suffix = "";
                    }
                    // Inter-Plan link
                    const newLink: Link = {
                        type: "output-to-input",
                        source: `${upstream.plan.planId}:${suffix}`,
                        target: `${plan.planId}:${input.path}`,
                        weight: 1,
                    };

                    if (fetchedLinks.some((item) => sameLink(item, newLink))) { continue; }
                    fetchedLinks.push(newLink);
                }
            }
            for (const output of plan.outputs) {
                for (const downstream of output.downstreams) {
                    await fetchPlan(downstream.plan.planId);

                    // Inter-Plan link
                    const newLink: Link = {
                        type: "output-to-input",
                        source: `${plan.planId}:${output.path}`,
                        target: `${downstream.plan.planId}:${downstream.mountpoint.path}`,
                        weight: 1,
                    };

                    if (fetchedLinks.some((item) => sameLink(item, newLink))) { continue; }
                    fetchedLinks.push(newLink);
                }
            }
            if (plan.log) {
                for (const downstream of plan.log.downstreams) {
                    await fetchPlan(downstream.plan.planId);

                    // Inter-Plan link
                    const newLink: Link = {
                        type: "output-to-input",
                        source: `${plan.planId}:log`,
                        target: `${downstream.plan.planId}:${downstream.mountpoint.path}`,
                        weight: 1,
                    };

                    if (fetchedLinks.some((item) => sameLink(item, newLink))) { continue; }
                    fetchedLinks.push(newLink);
                }
            }
        };

        // fetch Plan Graph from root Plan
        const fetchGraph = async () => {
            try {
                await fetchPlan(rootPlanId);

                const _nodes: ({ id: string } & NodeVariants)[] = [];

                // event handlers to highlight (elevate) nodes of the same Plan.
                // - onEnterNode is called when mouse enters the node to elevate, and
                // - onLeaveNode is called when mouse leaves the node to unelevate.

                const onEnterNode = (planId: string) => {
                    setNodes((prev) => {
                        return prev.map((node) => {
                            // NOTE: this switch statement is redundunt, but needed to satisfy TypeScript.
                            //
                            // The type of
                            //
                            //     {
                            //          ...node,
                            //        data: {
                            //            ...node.data,
                            //            variant: node.data.plan.planId === planId ? "elevation" : "outlined",
                            //        },
                            //     }
                            //
                            // does not satisfy Node & NodeVariants.
                            // So, we need to narrow the type explicitly.
                            //
                            switch (node.type) {
                                case "planNode":
                                    return {
                                        ...node,
                                        data: {
                                            ...node.data,
                                            variant: node.data.plan.planId === planId ? "elevation" : "outlined",
                                        },
                                    } satisfies Node & { data: PlanNodeValues };
                                case "inputNode":
                                    return {
                                        ...node,
                                        data: {
                                            ...node.data,
                                            variant: node.data.plan.planId === planId ? "elevation" : "outlined",
                                        },
                                    } satisfies Node & { data: InputNodeaValues };
                                case "outputNode":
                                    return {
                                        ...node,
                                        data: {
                                            ...node.data,
                                            variant: node.data.plan.planId === planId ? "elevation" : "outlined",
                                        },
                                    } satisfies Node & { data: OutputNodeValues };
                                case "logNode":
                                    return {
                                        ...node,
                                        data: {
                                            ...node.data,
                                            variant: node.data.plan.planId === planId ? "elevation" : "outlined",
                                        },
                                    } satisfies Node & { data: LogNodeValues };
                                default:
                                    throw new Error("Unexpected node type");
                            }
                        });
                    });
                }

                const onLeaveNode = () => {
                    setNodes((prev) => {
                        return prev.map((node) => {
                            // NOTE: this switch statement is redundunt, but needed to satisfy TypeScript.
                            //
                            // For detail, see the comment in onEnterNode.
                            //
                            switch (node.type) {
                                case "planNode":
                                    return {
                                        ...node,
                                        data: {
                                            ...node.data,
                                            variant: "outlined",
                                        },
                                    } satisfies Node & { data: PlanNodeValues };
                                case "inputNode":
                                    return {
                                        ...node,
                                        data: {
                                            ...node.data,
                                            variant: "outlined",
                                        },
                                    } satisfies Node & { data: InputNodeaValues };
                                case "outputNode":
                                    return {
                                        ...node,
                                        data: {
                                            ...node.data,
                                            variant: "outlined",
                                        },
                                    } satisfies Node & { data: OutputNodeValues };
                                case "logNode":
                                    return {
                                        ...node,
                                        data: {
                                            ...node.data,
                                            variant: "outlined",
                                        },
                                    } satisfies Node & { data: LogNodeValues };
                                default:
                                    throw new Error("Unexpected node type");
                            }
                        });
                    });
                };

                // set nodes and edges.
                for (const { plan } of fetchedPlan) {
                    _nodes.push({
                        id: plan.planId,
                        type: "planNode",
                        data: {
                            plan: plan,
                            onClick: (plan: PlanDetail) => {
                                setSelectedPlan(plan);
                                setSelectedPlanIsExpanded(false);
                            },
                            onEnter: onEnterNode,
                            onLeave: onLeaveNode,
                        },
                    });
                }

                for (const { plan, input } of fetchedInputs) {
                    _nodes.push({
                        id: `${plan.planId}:${input.path}`,
                        type: "inputNode",
                        data: {
                            plan, input,
                            onClick: (plan: PlanDetail) => {
                                setSelectedPlan(plan);
                                setSelectedPlanIsExpanded(false);
                            },
                            onEnter: onEnterNode,
                            onLeave: onLeaveNode,
                        },
                    });
                }

                for (const { plan, output } of fetchedOutputs) {
                    _nodes.push({
                        id: `${plan.planId}:${output.path}`,
                        type: "outputNode",
                        data: {
                            plan, output,
                            onClick: (plan: PlanDetail) => {
                                setSelectedPlan(plan);
                                setSelectedPlanIsExpanded(false);
                            },
                            onEnter: onEnterNode,
                            onLeave: onLeaveNode,
                        },
                    });
                }

                for (const { plan, log } of fetchedLogs) {
                    _nodes.push({
                        id: `${plan.planId}:log`,
                        type: "logNode",
                        data: {
                            plan, log,
                            onClick: (plan: PlanDetail) => {
                                setSelectedPlan(plan);
                                setSelectedPlanIsExpanded(false);
                            },
                            onEnter: onEnterNode,
                            onLeave: onLeaveNode,
                        },
                    });
                }

                const _edges = fetchedLinks.map((link) => {
                    return {
                        id: `${link.source}-${link.target}`,
                        animated: true,
                        selectable: false,
                        ...link
                    }
                });
                setEdges(_edges);

                // layout nodes provisionally.
                //
                // We do not know the size of nodes yet, so we use default size.
                // We will re-layout nodes when their dimensions are changed.
                const layoutedNodes = getLayoutedNodes(
                    _edges,
                    _nodes,
                    () => ({}), // default size
                );

                // set all nodes in one-shot.
                // This is needed to layout nodes correctly.
                setNodes(layoutedNodes.map((node) => ({
                    draggable: false,
                    // selectable should be true (default) to be clickable.
                    ...node,
                })));

            } catch (e) {
                console.error("Error fetching plan graph:", e);
            }
        };

        fetchGraph();
    }, [rootPlanId, planService]);

    const reactflow = useReactFlow();

    // this is need to avoid that selecting node invokes fitView
    const [fireFitView, setFireFitView] = React.useState(false);
    useEffect(() => { reactflow.fitView(); }, [fireFitView])

    return (
        <>
            <ReactFlow
                nodes={nodes}
                edges={edges}
                nodeTypes={nodeTypes}
                onNodesChange={(updatedNodes) => {
                    // this hook call is needed to avoid infinity loop.
                    onNodesChange(updatedNodes);

                    setNodes((prev) => {
                        // this event hook is called when nodes are "changed",
                        // added, removed, replaced, clicked(selected), or moved(dimensions, position).
                        //
                        // We are interested in the first "dimensions" change to layout nodes.
                        //
                        // Initial call of setNodes (in useEffect) ends up here with
                        // chainging "dimensions" of all nodes.
                        //
                        // On that timing, each nodes are measured and ready to be layouted.
                        const updated = updatedNodes.some((change) => (change.type === "dimensions"));
                        if (!updated) {
                            return prev
                        }

                        // Fire fitView only once (after layouted).
                        // We do not support resizing or moving nodes, so we can ignore further changes.
                        // Moreover, we do not want to fitView when selecting nodes or other user interactions.
                        // But, we need to fitView to make sure all nodes are visible at the first time.
                        setFireFitView(true);

                        return getLayoutedNodes(edges, prev, (node) => ({
                            width: node.measured?.width,
                            height: node.measured?.height,
                        }));
                    });
                }}
                onEdgesChange={onEdgesChange}
                fitView
                onClick={() => {
                    // delesect Plans
                    setSelectedPlan(null);
                    setSelectedPlanIsExpanded(false);
                }}
            >
                <Background color="#aaa" gap={16} />
                <Controls />
            </ReactFlow>
            <Collapse
                in={selectedPlan !== null}
                orientation="horizontal"
                sx={{ width: selectedPlan !== null ? "25vw" : undefined }}
            >
                <Box overflow="auto" height="100%">
                    {
                        selectedPlan &&
                        <PlanItem
                            plan={selectedPlan}
                            expanded={selectedPlanIsExpandedd}
                            setExpanded={(_, mode) => { setSelectedPlanIsExpanded(mode) }}
                        />
                    }
                </Box>
            </Collapse>
        </>
    );
};

const PlanGraph: React.FC<{
    planService: PlanService,
    rootPlanId: string,
}> = ({ planService, rootPlanId }) => {
    return (
        <Stack height="100%" direction="row" overflow="hidden">
            <ReactFlowProvider>
                <PlanGraphInner
                    planService={planService}
                    rootPlanId={rootPlanId}
                />
            </ReactFlowProvider>
        </Stack>
    );
};

export default PlanGraph;
