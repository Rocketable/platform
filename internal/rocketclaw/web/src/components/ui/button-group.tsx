import { cn } from "cn"

function ButtonGroup({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      role="group"
      data-slot="button-group"
      className={cn("flex w-fit items-stretch *:focus-visible:relative *:focus-visible:z-10 *:data-slot:rounded-r-none [&>[data-slot]:not(:has(~[data-slot]))]:rounded-r-lg! [&>[data-slot]~[data-slot]]:rounded-l-none [&>[data-slot]~[data-slot]]:border-l-0", className)}
      {...props}
    />
  )
}

export { ButtonGroup }
